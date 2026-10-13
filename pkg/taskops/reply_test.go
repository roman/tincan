package taskops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func runningReplyTask(t *testing.T, root string) *Service {
	t.Helper()
	ctx := context.Background()
	service, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, taskstate.Definition{
		ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "worker",
	}); err != nil {
		t.Fatal(err)
	}
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
	adoptStoredWorker(t, store, "task-1", worker, "tab-1")
	if _, err := service.Start(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	return service
}

func sendBoundReply(t *testing.T, root, messageRoot, generation string) inbox.Message {
	t.Helper()
	messages, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	worker, _ := inbox.ParseRecipient("task-1")
	concierge, _ := inbox.ParseRecipient("concierge")
	assignmentBody, err := json.Marshal(taskprotocol.Assignment{
		Kind: "task-assignment", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: generation, Body: "Do the work",
	})
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := messages.Send(worker, "assignment-1", string(assignmentBody))
	if err != nil {
		t.Fatal(err)
	}
	replyBody, err := json.Marshal(taskprotocol.Reply{
		Kind: "task-reply", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: generation, AssignmentID: assignment.ID.String(),
		ReplyKey: "answer-1", Body: "Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := messages.Send(concierge,
		"task-reply:"+root+":task-1:"+generation+":"+assignment.ID.String(),
		string(replyBody))
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestResolveBoundReplyFinishesAndAcknowledges(t *testing.T) {
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-1")
	for attempt := range 2 {
		result, err := service.Resolve(context.Background(), messageRoot, reply.ID.String(),
			taskstate.Close, "2026-10-11")
		if err != nil || result.TaskID != "task-1" || result.State != taskstate.Done ||
			result.Already != (attempt == 1) {
			t.Fatalf("resolve attempt %d = %#v, %v", attempt, result, err)
		}
	}
	messages, _ := inbox.New(messageRoot)
	concierge, _ := inbox.ParseRecipient("concierge")
	if _, err := messages.ReadHandled(concierge, reply.ID); err != nil {
		t.Fatalf("reply not acknowledged: %v", err)
	}
	disposition, err := messages.ReadDisposition(concierge, reply.ID)
	if err != nil || disposition.Action != "finish:task-1:gen-1:close:2026-10-11" {
		t.Fatalf("completion mode missing from disposition: %#v, %v", disposition, err)
	}
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil || task.State() != taskstate.Done || task.Snapshot().Completion == nil {
		t.Fatalf("resolved task = %#v, %v", task.Snapshot(), err)
	}
}

func TestResolveRejectsStaleGenerationBeforeAcknowledgement(t *testing.T) {
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-2")
	if _, err := service.Resolve(context.Background(), messageRoot, reply.ID.String(),
		taskstate.Close, "2026-10-11"); err == nil {
		t.Fatal("stale generation resolved")
	}
	messages, _ := inbox.New(messageRoot)
	concierge, _ := inbox.ParseRecipient("concierge")
	if _, err := messages.Read(concierge, reply.ID); err != nil {
		t.Fatalf("stale reply was acknowledged: %v", err)
	}
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil || task.State() != taskstate.InFlight {
		t.Fatalf("stale reply changed task: %#v, %v", task.Snapshot(), err)
	}
	if _, err := messages.ReadHandled(concierge, reply.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale reply handled: %v", err)
	}
}

func TestResolveRecoversResultSavedBeforeDispositionCompletion(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-1")
	messages, _ := inbox.New(messageRoot)
	concierge, _ := inbox.ParseRecipient("concierge")
	if _, _, err := messages.BeginDisposition(concierge, reply.ID,
		"finish:task-1:gen-1:close:2026-10-11"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	result, err := service.Resolve(
		ctx,
		messageRoot,
		reply.ID.String(),
		taskstate.Close,
		"2026-10-11",
	)
	if err != nil || !result.Already || result.State != taskstate.Done {
		t.Fatalf("resolve after saved result = %#v, %v", result, err)
	}
	if _, err := messages.ReadHandled(concierge, reply.ID); err != nil {
		t.Fatalf("recovered reply not acknowledged: %v", err)
	}
}

func TestResolveRejectsUnboundReply(t *testing.T) {
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	messages, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	concierge, _ := inbox.ParseRecipient("concierge")
	body, err := json.Marshal(taskprotocol.Reply{
		Kind: "task-reply", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: "gen-1", Body: "Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := messages.Send(concierge, "unbound", string(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(context.Background(), messageRoot, reply.ID.String(),
		taskstate.Close, "2026-10-11"); !errors.Is(err, taskprotocol.ErrInvalidReply) {
		t.Fatalf("unbound reply error = %v", err)
	}
	if _, err := messages.Read(concierge, reply.ID); err != nil {
		t.Fatalf("unbound reply was acknowledged: %v", err)
	}
}

func TestResolveRetainAfterNewWorkerStarts(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-1")
	messages, _ := inbox.New(messageRoot)
	concierge, _ := inbox.ParseRecipient("concierge")
	if _, _, err := messages.BeginDisposition(concierge, reply.ID, "finish:task-1:gen-1:retain"); err != nil {
		t.Fatal(err)
	}
	store, _ := taskstate.NewStore(root)
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.PlaceHold(taskstate.Hold{Reason: "More work", Kind: "captain"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Retain, ""); err != nil {
		t.Fatal(err)
	}
	newWorker := taskstate.Worker{
		Name: "worker-2", Harness: "codex", Session: "work", Pane: "pane-2",
		Project: "/project", Worktree: "/worktree", Generation: "gen-2",
		AgentArgs: []string{"resume", "thread-2"},
	}
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		retired, err := task.RetireResult("gen-1")
		if err != nil {
			return taskstate.Task{}, err
		}
		released, err := retired.ReleaseHold()
		if err != nil {
			return taskstate.Task{}, err
		}
		return released, nil
	}); err != nil {
		t.Fatal(err)
	}
	adoptStoredWorker(t, store, "task-1", newWorker, "tab-2")
	if _, err := service.Start(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.PlaceHold(taskstate.Hold{Reason: "Third pass", Kind: "captain"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-2", taskstate.Retain, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.RetireResult("gen-2")
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Resolve(ctx, messageRoot, reply.ID.String(), taskstate.Retain, "")
	if err != nil || !result.Already || result.State != taskstate.Queued {
		t.Fatalf("retained reply recovery = %#v, %v", result, err)
	}
	current, err := store.Read(ctx, "task-1")
	if err != nil || current.State() != taskstate.Queued ||
		!current.HasReceipt("gen-2", taskstate.Retain) {
		t.Fatalf("new worker changed = %#v, %v", current.Snapshot(), err)
	}
	if _, err := messages.ReadHandled(concierge, reply.ID); err != nil {
		t.Fatalf("old reply not acknowledged: %v", err)
	}
}

func TestResolveRejectsChangedCompletionMode(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-1")
	messages, _ := inbox.New(messageRoot)
	concierge, _ := inbox.ParseRecipient("concierge")
	store, _ := taskstate.NewStore(root)
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.PlaceHold(taskstate.Hold{Reason: "More work", Kind: "captain"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := messages.BeginDisposition(concierge, reply.ID,
		"finish:task-1:gen-1:close:2026-10-11"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, messageRoot, reply.ID.String(), taskstate.Retain, ""); !errors.Is(
		err,
		inbox.ErrDispositionConflict,
	) {
		t.Fatalf("changed mode error = %v", err)
	}
	current, err := store.Read(ctx, "task-1")
	if err != nil || current.State() != taskstate.InFlight {
		t.Fatalf("changed mode updated task: %#v, %v", current.Snapshot(), err)
	}
}

func TestResolveCanRetainAfterRejectedClose(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-1")
	store, _ := taskstate.NewStore(root)
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.PlaceHold(taskstate.Hold{Reason: "More work", Kind: "captain"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, messageRoot, reply.ID.String(),
		taskstate.Close, "2026-10-11"); !errors.Is(err, taskstate.ErrInvalidTransition) {
		t.Fatalf("held close error = %v", err)
	}
	messages, _ := inbox.New(messageRoot)
	concierge, _ := inbox.ParseRecipient("concierge")
	if _, err := messages.ReadDisposition(concierge, reply.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected close recorded intent: %v", err)
	}
	result, err := service.Resolve(ctx, messageRoot, reply.ID.String(), taskstate.Retain, "")
	if err != nil || result.State != taskstate.Queued {
		t.Fatalf("retain after rejected close = %#v, %v", result, err)
	}
}

func TestResolveRejectsChangedCloseDate(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-1")
	if _, err := service.Resolve(ctx, messageRoot, reply.ID.String(),
		taskstate.Close, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, messageRoot, reply.ID.String(),
		taskstate.Close, "2026-10-11"); !errors.Is(err, inbox.ErrDispositionConflict) {
		t.Fatalf("changed close date error = %v", err)
	}
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(ctx, "task-1")
	if err != nil || task.Definition().Closed != "2026-10-10" {
		t.Fatalf("close date changed: %#v, %v", task.Snapshot(), err)
	}
}

func TestResolveRejectsDateMismatchAfterSavedResult(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-1")
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, messageRoot, reply.ID.String(),
		taskstate.Close, "2026-10-11"); !errors.Is(err, taskstate.ErrInvalidTransition) {
		t.Fatalf("saved date mismatch error = %v", err)
	}
}

func TestResolveRejectsRetainDateAfterRetirement(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service := runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	reply := sendBoundReply(t, root, messageRoot, "gen-1")
	store, _ := taskstate.NewStore(root)
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.PlaceHold(taskstate.Hold{Reason: "More work", Kind: "captain"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Retain, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.RetireResult("gen-1")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, messageRoot, reply.ID.String(),
		taskstate.Retain, "2026-10-11"); !errors.Is(err, taskstate.ErrInvalidTransition) {
		t.Fatalf("retained result accepted a close date: %v", err)
	}
}

func TestResolveDoesNotReportUnappliedPartialResult(t *testing.T) {
	for _, mode := range []string{"disposition conflict", "finish rejection"} {
		t.Run(mode, func(t *testing.T) {
			root := testRoot(t)
			service := runningReplyTask(t, root)
			messageRoot := t.TempDir()
			reply := sendBoundReply(t, root, messageRoot, "gen-1")
			if mode == "disposition conflict" {
				messages, _ := inbox.New(messageRoot)
				recipient, _ := inbox.ParseRecipient("concierge")
				if _, _, err := messages.BeginDisposition(recipient, reply.ID, "finish:task-1:gen-1:close:2026-10-12"); err != nil {
					t.Fatal(err)
				}
				if _, err := messages.CompleteDisposition(recipient, reply.ID, "queued"); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.resolve(
				context.Background(),
				messageRoot,
				reply.ID.String(),
				taskstate.Close,
				"2026-10-12",
				func(context.Context, string, string, taskstate.CompletionMode, string) (taskstate.Task, error) {
					return taskstate.Task{}, taskstate.ErrInvalidTransition
				},
			)
			if err == nil || result.TaskID != "" {
				t.Fatalf("unapplied result %+v error %v", result, err)
			}
			store, _ := taskstate.NewStore(root)
			current, readErr := store.Read(context.Background(), "task-1")
			if readErr != nil || current.State() != taskstate.InFlight {
				t.Fatalf("task changed: %+v %v", current.Snapshot(), readErr)
			}
		})
	}
}
