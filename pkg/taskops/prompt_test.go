package taskops

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestTaskDoorbellPreservesRecordsAndPendingMessages(t *testing.T) {
	for _, outcome := range []string{"success", "prompt failure"} {
		t.Run(outcome, func(t *testing.T) {
			service, runner, root := runningWorkerService(t)
			ctx := context.Background()
			client := herdr.Client{SessionRunner: runner}
			worker, err := service.SupervisedWorker("task-1", root, client)
			if err != nil {
				t.Fatal(err)
			}
			message, err := worker.messages.Send(worker.recipient, "pending", "Durable work")
			if err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(service.root, "records", "task-1.json")
			before, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := os.Stat(record)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("prompt failed")
			if outcome == "prompt failure" {
				runner.promptError = failure
			}
			err = worker.Prompt(ctx, "task-1", notify.DoorbellText)
			if (outcome == "success" && err != nil) ||
				(outcome == "prompt failure" && !errors.Is(err, herdr.ErrCommand)) {
				t.Fatalf("Prompt = %v", err)
			}
			after, readErr := os.ReadFile(record)
			if readErr != nil {
				t.Fatal(readErr)
			}
			current, statErr := os.Stat(record)
			pending, pendingErr := worker.messages.Read(worker.recipient, message.ID)
			if statErr != nil || pendingErr != nil || !reflect.DeepEqual(pending, message) ||
				!bytes.Equal(before, after) ||
				!os.SameFile(selected, current) ||
				!reflect.DeepEqual(runner.promptTargets, []string{"pane-1"}) {
				t.Fatalf(
					"doorbell changed durable state: pending=%#v, errors %v/%v, targets %v",
					pending,
					statErr,
					pendingErr,
					runner.promptTargets,
				)
			}
		})
	}
}

func TestTaskDoorbellRejectsWrongInputAndIneligibleTask(t *testing.T) {
	for _, state := range []string{"wrong target", "wrong text", "queued", "completed", "no worker", "pending adoption", "pending generation", "pending launch"} {
		t.Run(state, func(t *testing.T) {
			service, runner, root := runningWorkerService(t)
			ctx := context.Background()
			worker, err := service.SupervisedWorker(
				"task-1",
				root,
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			target, text := "task-1", notify.DoorbellText
			switch state {
			case "wrong target":
				target = "pane-1"
			case "wrong text":
				text = "Arbitrary instructions"
			case "completed":
				if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					switch state {
					case "queued":
						snapshot.State = taskstate.Queued
					case "no worker":
						snapshot.State = taskstate.Queued
						snapshot.Worker = nil
					case "pending adoption":
						snapshot.State = taskstate.Queued
						snapshot.Worker.PendingAdoption = true
						snapshot.Worker.Workspace = ""
						snapshot.Worker.Tab = ""
					case "pending generation":
						snapshot.Worker.PendingGeneration = "gen-next"
					case "pending launch":
						snapshot.Worker.PendingLaunch = "gen-next"
					}
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := worker.Prompt(ctx, target, text); err == nil {
				t.Fatal("invalid doorbell accepted")
			}
			if runner.calls != 0 {
				t.Fatalf("invalid doorbell contacted Herdr %d times", runner.calls)
			}
		})
	}
}

func TestTaskDoorbellUsesReplacementWorkersCurrentEndpoint(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	ctx := context.Background()
	client := herdr.Client{SessionRunner: runner}
	worker, err := service.SupervisedWorker("task-1", root, client)
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := service.SendAssignment(ctx, root, "task-1", "work", "Original work", client)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Prompt(ctx, "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.PlaceHold(taskstate.Hold{Reason: "Replace worker"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Retain, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{SessionRunner: &retirementRunner{response: `{"error":{"code":"pane_not_found"}}`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.Update(ctx, "task-1", taskstate.Task.ReleaseHold); err != nil {
		t.Fatal(err)
	}
	runner.pane = "pane-2"
	runner.harness = "claude"
	runner.worktree = t.TempDir()
	adoptStoredWorker(
		t,
		service.store,
		"task-1",
		taskstate.Worker{
			Name:       "worker-2",
			Harness:    "claude",
			Session:    "work",
			Pane:       "pane-2",
			Project:    service.root,
			Worktree:   runner.worktree,
			Generation: "gen-2",
			AgentArgs:  []string{"resume", "thread-2"},
		},
		"tab-2",
	)
	if _, err := service.Start(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	if err := worker.Prompt(ctx, "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.promptTargets, []string{"pane-1", "pane-1", "pane-2"}) {
		t.Fatalf("targets = %v", runner.promptTargets)
	}
	pending, err := worker.messages.Read(worker.recipient, assignment.Message.ID)
	if err != nil || pending.ID != assignment.Message.ID {
		t.Fatalf("old assignment changed: %#v, %v", pending, err)
	}
	read, err := service.ReadAssignment(
		ctx,
		root,
		"task-1",
		assignment.Message.ID.String(),
		"gen-2",
	)
	if err != nil || read.State != AssignmentStale || read.Body != "" {
		t.Fatalf("old assignment read = %#v, %v", read, err)
	}
}

func TestTaskDoorbellRejectsEndpointOrRecordChanges(t *testing.T) {
	for _, change := range []string{"worktree", "harness", "record"} {
		t.Run(change, func(t *testing.T) {
			service, runner, root := runningWorkerService(t)
			worker, err := service.SupervisedWorker(
				"task-1",
				root,
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "worktree":
				runner.worktree = t.TempDir()
			case "harness":
				runner.onAgentRead = func() { runner.harness = "claude" }
			case "record":
				runner.onAgentRead = func() {
					record := filepath.Join(service.root, "records", "task-1.json")
					data, err := os.ReadFile(record)
					if err != nil {
						t.Fatal(err)
					}
					replacement := filepath.Join(service.root, "new-record.json")
					if err := os.WriteFile(replacement, data, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, record); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err == nil ||
				runner.prompts != 0 {
				t.Fatalf("changed binding prompt = %v, prompts=%d", err, runner.prompts)
			}
		})
	}
}

func TestTaskDoorbellHoldsTaskLockThroughPrompt(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	worker, err := service.SupervisedWorker("task-1", root, herdr.Client{SessionRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	runner.onPrompt = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, err := service.store.Update(ctx, "task-1", taskstate.Task.ReleaseHold); !errors.Is(
			err,
			context.DeadlineExceeded,
		) {
			t.Errorf("concurrent transition = %v", err)
		}
	}
	if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
		t.Fatal(err)
	}
}

func TestSendAssignmentPreservesMessageWhenFinalBindingCheckFails(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	runner.onAgentRead = func() {
		record := filepath.Join(service.root, "records", "task-1.json")
		data, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(service.root, "new-record.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, record); err != nil {
			t.Fatal(err)
		}
	}
	result, err := service.SendAssignment(
		context.Background(),
		root,
		"task-1",
		"work",
		"Durable work",
		herdr.Client{SessionRunner: runner},
	)
	if err != nil || result.Message.ID.String() == "" ||
		result.Notification != NotificationFailed ||
		!errors.Is(result.NotificationError, taskstate.ErrInvalidTask) ||
		runner.prompts != 0 {
		t.Fatalf("uncertain binding result = %#v, %v", result, err)
	}
	store, _ := inbox.New(root)
	pending, err := store.Read(result.Message.Recipient, result.Message.ID)
	if err != nil || !reflect.DeepEqual(pending, result.Message) {
		t.Fatalf("durable assignment = %#v, %v", pending, err)
	}
}
