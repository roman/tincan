package taskops

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestRestartPreservesPublishedReplyAcrossGenerations(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	ctx := context.Background()
	client := herdr.Client{SessionRunner: runner}
	assignment, err := service.SendAssignment(ctx, messageRoot, "task-1", "work", "Do work", client)
	if err != nil {
		t.Fatal(err)
	}
	input := ReplyInput{
		TaskID:       "task-1",
		Generation:   "gen-1",
		Key:          "result",
		AssignmentID: assignment.Message.ID.String(),
		Body:         "Published result",
	}
	reply, err := service.SendReply(ctx, messageRoot, input)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := service.SupervisedWorker("task-1", messageRoot, client)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		runner.absent = true
		if err := worker.Restart(ctx); err != nil {
			t.Fatal(err)
		}
	}
	current, err := service.store.Read(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	checked, err := service.CheckReply(ctx, messageRoot, reply.Message.ID.String())
	if err != nil || checked.State != ReplyCurrent || checked.Body != input.Body {
		t.Fatalf("restart stranded published reply: %+v, %v", checked, err)
	}
	if checked.Generation != current.Snapshot().Worker.Generation {
		t.Fatalf("reply inspection selected an obsolete stop generation: %+v", checked)
	}
	retry, err := service.SendReply(ctx, messageRoot, input)
	if err != nil || retry.Message.ID != reply.Message.ID {
		t.Fatalf("existing old reply retry changed: %+v, %v", retry, err)
	}
	notices, err := worker.messages.List(service.primaryRecipient)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range notices {
		var notice struct {
			Kind    string   `json:"kind"`
			Replies []string `json:"preserved_replies"`
		}
		if json.Unmarshal([]byte(message.Body), &notice) != nil || notice.Kind != "task-restart" {
			continue
		}
		count++
		found := false
		for _, id := range notice.Replies {
			if id == reply.Message.ID.String() {
				found = true
			}
		}
		if !found {
			t.Fatal("restart notification omitted a preserved reply")
		}
	}
	if count != 2 {
		t.Fatalf("restart notices = %d", count)
	}
	input.AssignmentID = ""
	input.Key = "fresh-old-reply"
	input.Body = "Must be rejected"
	if _, err := service.SendReply(ctx, messageRoot, input); err == nil {
		t.Fatal("accepted fresh publication from the old generation")
	}
	unlisted := sendBoundReply(t, service.root, messageRoot, "gen-1")
	stale, err := service.CheckReply(ctx, messageRoot, unlisted.ID.String())
	if err != nil || stale.State != ReplyStale || stale.Body != "" {
		t.Fatalf("unlisted old reply became actionable: %+v, %v", stale, err)
	}
	if _, err := service.Resolve(ctx, messageRoot, unlisted.ID.String(), taskstate.Close, "2026-10-13"); err == nil {
		t.Fatal("accepted an unlisted old reply")
	}
	result, err := service.Resolve(
		ctx,
		messageRoot,
		reply.Message.ID.String(),
		taskstate.Close,
		"2026-10-13",
	)
	if err != nil || result.State != taskstate.Done {
		t.Fatalf("preserved reply could not be accepted: %+v, %v", result, err)
	}
	accepted, err := service.store.Read(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Snapshot().Completion == nil ||
		accepted.Snapshot().Completion.Generation != current.Snapshot().Worker.Generation {
		t.Fatal("acceptance did not bind the current worker for stop and cleanup")
	}
	for _, phase := range []string{"accepted", "retired"} {
		if phase == "retired" {
			if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
				return task.RetireResult(current.Snapshot().Worker.Generation)
			}); err != nil {
				t.Fatal(err)
			}
		}
		retry, err := service.Resolve(
			ctx,
			messageRoot,
			reply.Message.ID.String(),
			taskstate.Close,
			"2026-10-13",
		)
		if err != nil || !retry.Already {
			t.Fatalf("%s acceptance retry lost its generation: %+v, %v", phase, retry, err)
		}
	}
}

func TestRestartRedeliversUnfinishedAssignment(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	ctx := context.Background()
	client := herdr.Client{SessionRunner: runner}
	original, err := service.SendAssignment(
		ctx,
		messageRoot,
		"task-1",
		"work",
		"Do unfinished work",
		client,
	)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := service.SupervisedWorker("task-1", messageRoot, client)
	if err != nil {
		t.Fatal(err)
	}
	runner.absent = true
	if err := worker.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	task, err := service.store.Read(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	generation := task.Snapshot().Worker.Generation
	messages, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	pending, err := messages.List(recipient)
	if err != nil {
		t.Fatal(err)
	}
	var deliveries []inbox.Message
	for _, message := range pending {
		assignment, err := taskprotocol.DecodeAssignment(message.Body, service.root, "task-1")
		if err == nil && assignment.SpawnGeneration == generation {
			deliveries = append(deliveries, message)
		}
	}
	if len(deliveries) != 1 {
		t.Fatalf("unfinished assignment has no unique current delivery: got %d", len(deliveries))
	}
	delivery := deliveries[0]
	if delivery.ID == original.Message.ID {
		t.Fatal("restart reused an obsolete generation's message")
	}
	read, err := service.ReadAssignment(
		ctx,
		messageRoot,
		"task-1",
		delivery.ID.String(),
		generation,
	)
	if err != nil || read.State != AssignmentCurrent {
		t.Fatalf("redelivery is not current: %+v, %v", read, err)
	}
	for _, part := range []string{"redelivery", original.Message.ID.String(), "prior effects", "Do unfinished work"} {
		if !strings.Contains(read.Body, part) {
			t.Fatalf("redelivery lacks %q: %s", part, read.Body)
		}
	}
	retried, err := service.SendAssignment(
		ctx,
		messageRoot,
		"task-1",
		"work",
		"Do unfinished work",
		client,
	)
	if err != nil || retried.Message.ID != delivery.ID {
		t.Fatalf("caller retry duplicated recovered work: %+v, %v", retried, err)
	}
	if _, err := service.SendAssignment(ctx, messageRoot, "task-1", "work", "Changed work", client); err == nil {
		t.Fatal("changed caller body reused recovered assignment")
	}
}

func TestRestartKeepsStableOriginAndClosedCallerKeys(t *testing.T) {
	for _, state := range []string{"pending", "acknowledged", "replied"} {
		t.Run(state, func(t *testing.T) {
			service, runner, messageRoot := runningWorkerService(t)
			ctx := context.Background()
			client := herdr.Client{SessionRunner: runner}
			original, err := service.SendAssignment(
				ctx,
				messageRoot,
				"task-1",
				"work",
				"Do work",
				client,
			)
			if err != nil {
				t.Fatal(err)
			}
			worker, _ := service.SupervisedWorker("task-1", messageRoot, client)
			previous := original.Message.ID.String()
			for iteration := range 2 {
				runner.absent = true
				if err := worker.Restart(ctx); err != nil {
					t.Fatal(err)
				}
				current, _ := service.store.Read(ctx, "task-1")
				generation := current.Snapshot().Worker.Generation
				retry, err := service.SendAssignment(
					ctx,
					messageRoot,
					"task-1",
					"work",
					"Do work",
					client,
				)
				if err != nil {
					t.Fatal(err)
				}
				assignment, err := taskprotocol.ReadAssignment(
					worker.messages,
					service.root,
					"task-1",
					retry.Message.ID,
				)
				if err != nil {
					t.Fatal(err)
				}
				if iteration == 0 || state == "pending" {
					if assignment.OriginalMessageID != original.Message.ID.String() ||
						assignment.PreviousMessageID != previous ||
						assignment.SpawnGeneration != generation {
						t.Fatalf("lineage = %+v", assignment)
					}
				} else if retry.Message.ID.String() != previous {
					t.Fatal("caller retry reopened handled work")
				}
				previous = retry.Message.ID.String()
				if state == "acknowledged" {
					if _, err := service.AcknowledgeAssignment(ctx, messageRoot, "task-1", previous, generation); iteration == 0 &&
						err != nil {
						t.Fatal(err)
					}
				}
				if state == "replied" && iteration == 0 {
					if _, err := service.SendReply(ctx, messageRoot, ReplyInput{TaskID: "task-1", Generation: generation, AssignmentID: previous, Key: "result", Body: "Done"}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := service.SendAssignment(ctx, messageRoot, "task-1", "work", "Changed work", client); err == nil {
					t.Fatal("changed body reused a caller key")
				}
			}
		})
	}
}

func TestRestartBlocksUnfinishedAcceptance(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	ctx := context.Background()
	reply := sendBoundReply(t, service.root, messageRoot, "gen-1")
	messages, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("concierge")
	if _, _, err := messages.BeginDisposition(recipient, reply.ID, "finish:task-1:gen-1:close:2026-10-13"); err != nil {
		t.Fatal(err)
	}
	worker, _ := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	runner.absent = true
	if err := worker.Restart(ctx); err == nil {
		t.Fatal("restart advanced a generation with an unfinished acceptance")
	}
	current, _ := service.store.Read(ctx, "task-1")
	if current.Snapshot().Worker.PendingLaunch != "" ||
		current.Snapshot().Worker.Generation != "gen-1" {
		t.Fatal("acceptance lost its reserved generation")
	}
	if _, err := service.Resolve(ctx, messageRoot, reply.ID.String(), taskstate.Close, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
}

func TestRestartRecoveryRetriesFrozenDelivery(t *testing.T) {
	for _, phase := range []string{"replacement published", "predecessor acknowledged", "launch unconfirmed"} {
		t.Run(phase, func(t *testing.T) {
			service, runner, messageRoot := runningWorkerService(t)
			ctx := context.Background()
			client := herdr.Client{SessionRunner: runner}
			original, err := service.SendAssignment(
				ctx,
				messageRoot,
				"task-1",
				"work",
				"Do work",
				client,
			)
			if err != nil {
				t.Fatal(err)
			}
			worker, _ := service.SupervisedWorker("task-1", messageRoot, client)
			_, err = service.store.WithTask(ctx, "task-1", func(task *taskstate.LockedTask) error {
				if _, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) { return current.ReserveLaunch("gen-2") }); err != nil {
					return err
				}
				return worker.reserveRestartRecovery(task, "gen-2")
			})
			if err != nil {
				t.Fatal(err)
			}
			envelope := taskprotocol.Assignment{
				Kind:              "task-assignment",
				State:             filepath.Join(service.root, "tasks"),
				Task:              "task-1",
				SpawnGeneration:   "gen-2",
				Body:              "Do work",
				OriginalMessageID: original.Message.ID.String(),
				PreviousMessageID: original.Message.ID.String(),
			}
			body, _ := json.Marshal(envelope)
			replacement, err := worker.messages.Send(
				worker.recipient,
				taskprotocol.RedeliveryKey(
					service.root,
					"task-1",
					"gen-2",
					original.Message.ID.String(),
				),
				string(body),
			)
			if err != nil {
				t.Fatal(err)
			}
			if phase != "replacement published" {
				if err := worker.messages.Acknowledge(worker.recipient, original.Message.ID); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "launch unconfirmed" {
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.AdvanceGeneration("gen-2") }); err != nil {
					t.Fatal(err)
				}
				// The native launch succeeded before its confirmation reached durable state.
				if err := worker.Observe(ctx); err != nil {
					t.Fatal(err)
				}
				if runner.starts != 0 {
					t.Fatal("observation launched a duplicate worker")
				}
			} else {
				runner.absent = true
				if err := worker.Restart(ctx); err != nil {
					t.Fatal(err)
				}
			}
			retry, err := service.SendAssignment(
				ctx,
				messageRoot,
				"task-1",
				"work",
				"Do work",
				client,
			)
			if err != nil || retry.Message.ID != replacement.ID ||
				retry.Message.Body != replacement.Body ||
				!retry.Message.CreatedAt.Equal(replacement.CreatedAt) {
				t.Fatalf("recovery changed the delivery: %+v, %v", retry, err)
			}
			current, _ := service.store.Read(ctx, "task-1")
			if current.Snapshot().Worker.PendingLaunch != "" {
				t.Fatal("recovery left an unconfirmed reservation")
			}
			notices, err := worker.messages.List(service.primaryRecipient)
			if err != nil || len(notices) != 1 {
				t.Fatalf("restart notice missing: %+v, %v", notices, err)
			}
			var notice struct {
				Kind        string   `json:"kind"`
				Assignments []string `json:"redelivered_assignments"`
			}
			if err := json.Unmarshal([]byte(notices[0].Body), &notice); err != nil ||
				notice.Kind != "task-restart" ||
				len(notice.Assignments) != 1 ||
				notice.Assignments[0] != replacement.ID.String() {
				t.Fatalf("restart notice = %+v, %v", notice, err)
			}
		})
	}
}

func TestRestartKeepsPublishedReplyAfterInterruptedAssignmentAck(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	ctx := context.Background()
	reply := sendBoundReply(t, service.root, messageRoot, "gen-1")
	worker, _ := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	runner.absent = true
	if err := worker.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := service.store.Read(ctx, "task-1")
	plan := current.Snapshot().Worker.Restarts[0]
	if len(plan.Assignments) != 0 || len(plan.Replies) != 1 ||
		plan.Replies[0] != reply.ID.String() {
		t.Fatalf("published reply was redelivered as work: %+v", plan)
	}
	checked, err := service.CheckReply(ctx, messageRoot, reply.ID.String())
	if err != nil || checked.State != ReplyCurrent {
		t.Fatalf("reply lost after interrupted acknowledgement: %+v, %v", checked, err)
	}
}

func TestRestartKeepsIndependentUnkeyedAssignments(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	ctx := context.Background()
	client := herdr.Client{SessionRunner: runner}
	first, err := service.SendAssignment(ctx, messageRoot, "task-1", "", "Do work", client)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SendAssignment(ctx, messageRoot, "task-1", "", "Do work", client)
	if err != nil || first.Message.ID == second.Message.ID {
		t.Fatalf("independent assignments merged: %+v, %v", second, err)
	}
	worker, _ := service.SupervisedWorker("task-1", messageRoot, client)
	runner.absent = true
	if err := worker.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := service.store.Read(ctx, "task-1")
	pending, err := worker.messages.List(worker.recipient)
	if err != nil {
		t.Fatal(err)
	}
	origins := map[string]bool{}
	for _, message := range pending {
		assignment, err := taskprotocol.ReadAssignment(
			worker.messages,
			service.root,
			"task-1",
			message.ID,
		)
		if err != nil {
			continue
		}
		if assignment.SpawnGeneration != current.Snapshot().Worker.Generation {
			t.Fatal("obsolete assignment still pending")
		}
		origins[assignment.OriginalMessageID] = true
	}
	if len(origins) != 2 || !origins[first.Message.ID.String()] ||
		!origins[second.Message.ID.String()] {
		t.Fatalf("independent redeliveries = %+v", origins)
	}
}

func TestRestartSerializesMailboxOperations(t *testing.T) {
	for _, operation := range []string{"publish reply", "accept reply", "acknowledge assignment"} {
		t.Run(operation, func(t *testing.T) {
			service, runner, messageRoot := runningWorkerService(t)
			ctx := context.Background()
			client := herdr.Client{SessionRunner: runner}
			assignment, err := service.SendAssignment(
				ctx,
				messageRoot,
				"task-1",
				"work",
				"Do work",
				client,
			)
			if err != nil {
				t.Fatal(err)
			}
			reply := sendBoundReply(t, service.root, messageRoot, "gen-1")
			worker, _ := service.SupervisedWorker("task-1", messageRoot, client)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			runner.onStart = func() error { close(entered); <-release; return nil }
			runner.absent = true
			restarted := make(chan error, 1)
			go func() { restarted <- worker.Restart(ctx) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("restart did not reach the launch boundary")
			}
			blocked, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
			defer cancel()
			perform := func(ctx context.Context) error {
				switch operation {
				case "publish reply":
					_, err := service.SendReply(
						ctx,
						messageRoot,
						ReplyInput{
							TaskID:       "task-1",
							Generation:   "gen-1",
							AssignmentID: assignment.Message.ID.String(),
							Key:          "late-result",
							Body:         "Late result",
						},
					)
					return err
				case "accept reply":
					_, err := service.Resolve(
						ctx,
						messageRoot,
						reply.ID.String(),
						taskstate.Close,
						"2026-10-13",
					)
					return err
				default:
					_, err := service.AcknowledgeAssignment(
						ctx,
						messageRoot,
						"task-1",
						assignment.Message.ID.String(),
						"gen-1",
					)
					return err
				}
			}
			if err := perform(blocked); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("mailbox operation bypassed the restart lock: %v", err)
			}
			releaseOnce.Do(func() { close(release) })
			if err := <-restarted; err != nil {
				t.Fatal(err)
			}
			err = perform(ctx)
			if operation == "publish reply" && err == nil {
				t.Fatal("old generation published after restart")
			}
			if operation != "publish reply" && err != nil {
				t.Fatal(err)
			}
		})
	}
}
