package taskops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestStalledAssignmentBoundsPromptsAndAlertsPrimary(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	runner.agentStatus = herdr.Done
	runner.screen = "You're out of usage credits. Run /usage-credits or /model."
	worker, err := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	now := started
	worker.now = func() time.Time { return now }
	result, err := worker.sendAssignment(
		context.Background(),
		"work",
		"Do work",
		func(recipient inbox.Recipient, key, body string) (inbox.Message, error) {
			message, err := worker.messages.Send(recipient, key, body)
			started, now = message.CreatedAt, message.CreatedAt
			return message, err
		},
	)
	if err != nil || result.Notification != NotificationSent || runner.prompts != 1 {
		t.Fatalf("first prompt = %+v, %v; prompts=%d", result, err, runner.prompts)
	}
	for _, step := range []struct {
		elapsed time.Duration
		prompts int
	}{
		{0, 1}, {4 * time.Second, 1}, {5 * time.Second, 2}, {14 * time.Second, 2}, {15 * time.Second, 3}, {time.Minute, 3}, {5*time.Minute - time.Nanosecond, 3}, {5 * time.Minute, 3},
	} {
		now = started.Add(step.elapsed)
		if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
			t.Fatal(err)
		}
		if runner.prompts != step.prompts {
			t.Fatalf("after %s: prompts=%d, want %d", step.elapsed, runner.prompts, step.prompts)
		}
	}
	notices, err := worker.messages.List(service.primaryRecipient)
	if err != nil || len(notices) != 1 {
		t.Fatalf("primary stall notices = %+v, %v", notices, err)
	}
	var notice struct {
		Kind       string `json:"kind"`
		Task       string `json:"task"`
		Generation string `json:"generation"`
		Assignment string `json:"assignment_id"`
		State      string `json:"observed_state"`
		Screen     string `json:"observed_screen"`
	}
	if err := json.Unmarshal([]byte(notices[0].Body), &notice); err != nil {
		t.Fatal(err)
	}
	if notice.Kind != "task-stalled" || notice.Task != "task-1" || notice.Generation != "gen-1" ||
		notice.Assignment != result.Message.ID.String() ||
		notice.State != "done" ||
		!strings.Contains(notice.Screen, "out of usage credits") {
		t.Fatalf("stall notice = %+v", notice)
	}
	// Reconstructing a controller must preserve its prompt budget and frozen notice.
	replacement, err := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	replacement.now = func() time.Time { return now.Add(time.Minute) }
	runner.screen = "The screen changed after the alert."
	if err := replacement.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
	retried, err := worker.messages.List(service.primaryRecipient)
	if err != nil || len(retried) != 1 || retried[0].ID != notices[0].ID ||
		retried[0].Body != notices[0].Body ||
		runner.prompts != 3 {
		t.Fatalf(
			"controller restart reset the stall episode: %+v, %v; prompts=%d",
			retried,
			err,
			runner.prompts,
		)
	}
}

func TestAssignmentRetryDefersAnIneffectivePrompt(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	runner.agentStatus = herdr.Done
	worker, err := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Second)
	worker.now = func() time.Time { return now }
	first, err := worker.sendAssignment(
		context.Background(),
		"work",
		"Do work",
		worker.messages.Send,
	)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := worker.sendAssignment(
		context.Background(),
		"work",
		"Do work",
		worker.messages.Send,
	)
	if err != nil || retry.Message.ID != first.Message.ID ||
		retry.Notification != NotificationDeferred ||
		runner.prompts != 1 {
		t.Fatalf(
			"key retry repeated an ineffective prompt: %+v, %v; prompts=%d",
			retry,
			err,
			runner.prompts,
		)
	}
}

func TestStallDeadlineUsesPublishedAssignmentTime(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	runner.agentStatus = herdr.Done
	worker, err := service.SupervisedWorker("task-1", root, herdr.Client{SessionRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a controller stopping after publication but before recording a prompt.
	_, err = worker.sendAssignment(
		context.Background(),
		"old",
		"Do work",
		func(recipient inbox.Recipient, key, body string) (inbox.Message, error) {
			message, err := worker.messages.Send(recipient, key, body)
			if err != nil {
				return message, err
			}
			return message, context.Canceled
		},
	)
	if err == nil {
		t.Fatal("publication interruption succeeded")
	}
	messages, err := worker.messages.List(worker.recipient)
	if err != nil || len(messages) != 1 {
		t.Fatalf("pending: %+v, %v", messages, err)
	}
	now := messages[0].CreatedAt.Add(stallDeadline - time.Second)
	worker.now = func() time.Time { return now }
	if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
	notices, err := worker.messages.List(service.primaryRecipient)
	if err != nil || len(notices) != 1 || runner.prompts != 1 {
		t.Fatalf(
			"deadline restarted after publication: notices=%+v, %v; prompts=%d",
			notices,
			err,
			runner.prompts,
		)
	}
}

func TestWorkingAssignmentDefersWithoutConsumingPromptBudget(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	runner.agentStatus = herdr.Working
	result, err := service.SendAssignment(
		context.Background(),
		root,
		"task-1",
		"work",
		"Do work",
		herdr.Client{SessionRunner: runner},
	)
	if err != nil || result.Notification != NotificationDeferred || runner.prompts != 0 {
		t.Fatalf("busy assignment: %+v, %v; prompts=%d", result, err, runner.prompts)
	}
	runner.agentStatus = herdr.Idle
	retry, err := service.SendAssignment(
		context.Background(),
		root,
		"task-1",
		"work",
		"Do work",
		herdr.Client{SessionRunner: runner},
	)
	if err != nil || retry.Notification != NotificationSent || runner.prompts != 1 {
		t.Fatalf("ready assignment: %+v, %v; prompts=%d", retry, err, runner.prompts)
	}
}

func TestHandledAssignmentClearsPromptBudgetImmediately(t *testing.T) {
	for _, operation := range []string{"ack", "reply"} {
		t.Run(operation, func(t *testing.T) {
			service, runner, root := runningWorkerService(t)
			result, err := service.SendAssignment(
				context.Background(),
				root,
				"task-1",
				"work",
				"Do work",
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "ack" {
				_, err = service.AcknowledgeAssignment(
					context.Background(),
					root,
					"task-1",
					result.Message.ID.String(),
					"gen-1",
				)
			} else {
				_, err = service.SendReply(context.Background(), root, ReplyInput{TaskID: "task-1", Generation: "gen-1", Key: "reply", AssignmentID: result.Message.ID.String(), Body: "Done"})
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := service.store.Read(context.Background(), "task-1")
			if err != nil || current.Snapshot().Worker.PromptProgress != nil {
				t.Fatalf("handled assignment retained budget: %+v, %v", current.Snapshot(), err)
			}
		})
	}
}

func TestStallAlertRetriesFrozenEvidenceAfterPublicationFailure(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	runner.agentStatus = herdr.Done
	runner.screen = "Original native error"
	worker, err := service.SupervisedWorker("task-1", root, herdr.Client{SessionRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	first, err := worker.sendAssignment(
		context.Background(),
		"work",
		"Do work",
		worker.messages.Send,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := first.Message.CreatedAt.Add(stallDeadline)
	worker.now = func() time.Time { return now }
	primary := filepath.Join(root, "inbox", service.primaryRecipient.String())
	if err := os.MkdirAll(primary, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(primary, "handled")
	if err := os.WriteFile(blocker, []byte("block send"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err == nil {
		t.Fatal("publication failure succeeded")
	}
	saved, err := service.store.Read(context.Background(), "task-1")
	if err != nil || saved.Snapshot().Worker.PromptProgress.Alert == nil {
		t.Fatalf("lost alert after send failure: %+v, %v", saved.Snapshot(), err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	runner.screen = "Changed error"
	now = now.Add(time.Minute)
	if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
	notices, err := worker.messages.List(service.primaryRecipient)
	if err != nil || len(notices) != 1 ||
		!strings.Contains(notices[0].Body, "Original native error") ||
		strings.Contains(notices[0].Body, "Changed error") ||
		runner.prompts != 1 {
		t.Fatalf("retry changed frozen evidence: %+v, %v; prompts=%d", notices, err, runner.prompts)
	}
}

func TestBlockedAssignmentAlertsWithoutPromptAttempt(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	runner.agentStatus = herdr.Blocked
	runner.screen = "Native approval required"
	worker, err := service.SupervisedWorker("task-1", root, herdr.Client{SessionRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	first, err := worker.sendAssignment(
		context.Background(),
		"work",
		"Do work",
		worker.messages.Send,
	)
	if err != nil || first.Notification != NotificationDeferred {
		t.Fatalf("blocked send: %+v, %v", first, err)
	}
	worker.now = func() time.Time { return first.Message.CreatedAt.Add(stallDeadline) }
	if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
	notices, err := worker.messages.List(service.primaryRecipient)
	if err != nil || len(notices) != 1 || runner.prompts != 0 {
		t.Fatalf(
			"blocked assignment never alerted: %+v, %v; prompts=%d",
			notices,
			err,
			runner.prompts,
		)
	}
}

func TestConfirmedReplyClearsBudgetWhenAcknowledgementFails(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	first, err := service.SendAssignment(
		context.Background(),
		root,
		"task-1",
		"work",
		"Do work",
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	handled := filepath.Join(root, "inbox", "task-1", "handled")
	if err := os.Rename(handled, handled+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handled, []byte("block acknowledgement"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := service.SendReply(
		context.Background(),
		root,
		ReplyInput{
			TaskID:       "task-1",
			Generation:   "gen-1",
			Key:          "reply",
			AssignmentID: first.Message.ID.String(),
			Body:         "Done",
		},
	)
	if err == nil || result.Message.ID.String() == "" ||
		result.Assignment != ReplyAcknowledgementUnconfirmed {
		t.Fatalf("ack failure: %+v, %v", result, err)
	}
	current, err := service.store.Read(context.Background(), "task-1")
	if err != nil || current.Snapshot().Worker.PromptProgress != nil {
		t.Fatalf("confirmed reply retained budget: %+v, %v", current.Snapshot(), err)
	}
}

func TestConfirmedReplyRetryClearsProgressAfterCompletion(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	first, err := service.SendAssignment(
		context.Background(),
		root,
		"task-1",
		"work",
		"Do work",
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.store.Read(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	input := ReplyInput{
		TaskID:       "task-1",
		Generation:   "gen-1",
		Key:          "reply",
		AssignmentID: first.Message.ID.String(),
		Body:         "Done",
	}
	reply, err := service.SendReply(context.Background(), root, input)
	if err != nil {
		t.Fatal(err)
	}
	// Restore the durable state left by a failed progress write after reply publication.
	if _, err := service.store.Update(context.Background(), "task-1", func(current taskstate.Task) (taskstate.Task, error) {
		snapshot := current.Snapshot()
		snapshot.Worker.PromptProgress = before.Snapshot().Worker.PromptProgress
		return taskstate.NewMigrated(snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(context.Background(), "task-1", "gen-1", taskstate.Close, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	retry, err := service.SendReply(context.Background(), root, input)
	if err != nil || retry.Message.ID != reply.Message.ID || retry.Assignment != ReplyAcknowledged {
		t.Fatalf("completed reply retry: %+v, %v", retry, err)
	}
}

func TestPendingAssignmentsKeepIndependentPromptBudgets(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	worker, err := service.SupervisedWorker("task-1", root, herdr.Client{SessionRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Second)
	worker.now = func() time.Time { return now }
	first, err := worker.sendAssignment(
		context.Background(),
		"first",
		"First work",
		worker.messages.Send,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := worker.sendAssignment(
		context.Background(),
		"second",
		"Second work",
		worker.messages.Send,
	)
	if err != nil || second.Notification != NotificationDeferred || runner.prompts != 1 {
		t.Fatalf(
			"new assignment reset oldest budget: %+v, %v; prompts=%d",
			second,
			err,
			runner.prompts,
		)
	}
	current, err := service.store.Read(context.Background(), "task-1")
	if err != nil ||
		current.Snapshot().Worker.PromptProgress.AssignmentID != first.Message.ID.String() {
		t.Fatalf("oldest selection: %+v, %v", current.Snapshot(), err)
	}
	if _, err := service.AcknowledgeAssignment(context.Background(), root, "task-1", first.Message.ID.String(), "gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
	current, err = service.store.Read(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	progress := current.Snapshot().Worker.PromptProgress
	if progress.AssignmentID != second.Message.ID.String() || progress.Attempts != 1 ||
		!progress.StartedAt.Equal(second.Message.CreatedAt) ||
		runner.prompts != 2 {
		t.Fatalf(
			"second assignment inherited old budget: %+v; prompts=%d",
			progress,
			runner.prompts,
		)
	}
}
