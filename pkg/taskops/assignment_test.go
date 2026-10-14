package taskops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestSendAssignmentRecordsBoundMessageAndRetries(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	ctx := context.Background()
	client := herdr.Client{SessionRunner: runner}
	first, err := service.SendAssignment(ctx, root, "task-1", "brief", "Do the work", client)
	if err != nil || first.Message.ID.String() == "" || first.Notification != NotificationSent {
		t.Fatalf("send = %#v, %v", first, err)
	}
	wantKey := fmt.Sprintf("task-assignment:%s:task-1:gen-1:brief", service.root)
	if first.Message.Key != wantKey || first.Message.Recipient.String() != "task-1" {
		t.Fatalf("message identity = %#v", first.Message)
	}
	envelope, err := taskprotocol.DecodeAssignment(first.Message.Body, service.root, "task-1")
	if err != nil || envelope.SpawnGeneration != "gen-1" || envelope.Body != "Do the work" {
		t.Fatalf("assignment = %#v, %v", envelope, err)
	}
	retry, err := service.SendAssignment(ctx, root, "task-1", "brief", "Do the work", client)
	if err != nil || retry.Message.ID != first.Message.ID ||
		retry.Notification != NotificationDeferred ||
		runner.prompts != 1 {
		t.Fatalf("retry = %#v, %v; prompts = %d", retry, err, runner.prompts)
	}
	store, _ := inbox.New(root)
	if err := store.Acknowledge(first.Message.Recipient, first.Message.ID); err != nil {
		t.Fatal(err)
	}
	handled, err := service.SendAssignment(ctx, root, "task-1", "brief", "Do the work", client)
	if err != nil || handled.Message.ID != first.Message.ID ||
		handled.Notification != NotificationAcknowledged ||
		runner.prompts != 1 {
		t.Fatalf("handled retry = %#v, %v", handled, err)
	}
	if _, err := service.SendAssignment(ctx, root, "task-1", "brief", "Different work", client); !errors.Is(
		err,
		inbox.ErrKeyConflict,
	) {
		t.Fatalf("conflicting retry = %v", err)
	}
	for range 2 {
		result, err := service.SendAssignment(ctx, root, "task-1", "", "Unkeyed", client)
		if err != nil || result.Message.Key != "" || result.Message.ID == first.Message.ID {
			t.Fatalf("unkeyed send = %#v, %v", result, err)
		}
	}
	pending, err := store.List(first.Message.Recipient)
	if err != nil || len(pending) != 2 || pending[0].ID == pending[1].ID {
		t.Fatalf("unkeyed messages = %#v, %v", pending, err)
	}
}

func TestSendAssignmentRejectsInactiveOrUnconfirmedWorker(t *testing.T) {
	for _, test := range []string{"completed", "pending adoption", "pending generation", "pending launch", "blank body"} {
		t.Run(test, func(t *testing.T) {
			service, runner, root := runningWorkerService(t)
			ctx := context.Background()
			if test == "completed" {
				if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
					t.Fatal(err)
				}
			} else if test != "blank body" {
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					switch test {
					case "pending adoption":
						snapshot.State = taskstate.Queued
						snapshot.Worker.PendingAdoption = true
						snapshot.Worker.Workspace = ""
						snapshot.Worker.Tab = ""
					case "pending generation":
						snapshot.Worker.PendingGeneration = "gen-2"
					case "pending launch":
						snapshot.Worker.PendingLaunch = "gen-2"
					}
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			body := "Do work"
			if test == "blank body" {
				body = " \n "
			}
			result, err := service.SendAssignment(
				ctx,
				root,
				"task-1",
				"brief",
				body,
				herdr.Client{SessionRunner: runner},
			)
			if err == nil || result.Message.ID.String() != "" || runner.calls != 0 {
				t.Fatalf("rejected send = %#v, %v; calls = %d", result, err, runner.calls)
			}
			store, _ := inbox.New(root)
			recipient, _ := inbox.ParseRecipient("task-1")
			pending, err := store.List(recipient)
			if err != nil || len(pending) != 0 {
				t.Fatalf("rejected messages = %#v, %v", pending, err)
			}
		})
	}
}

func TestSendAssignmentPreservesDeliveryWhenNotificationFails(t *testing.T) {
	for _, test := range []string{"directory", "harness", "missing", "prompt"} {
		t.Run(test, func(t *testing.T) {
			service, runner, root := runningWorkerService(t)
			switch test {
			case "directory":
				runner.worktree = t.TempDir()
			case "harness":
				runner.harness = "claude"
			case "missing":
				runner.absent = true
			case "prompt":
				runner.promptError = errors.New("prompt failed")
			}
			result, err := service.SendAssignment(
				context.Background(),
				root,
				"task-1",
				"brief",
				"Do work",
				herdr.Client{SessionRunner: runner},
			)
			if err != nil || result.Message.ID.String() == "" ||
				result.Notification != NotificationFailed ||
				result.NotificationError == nil {
				t.Fatalf("send = %#v, %v", result, err)
			}
			store, _ := inbox.New(root)
			if _, err := store.Read(result.Message.Recipient, result.Message.ID); err != nil {
				t.Fatalf("durable assignment missing: %v", err)
			}
			if test != "prompt" && runner.prompts != 0 {
				t.Fatal("prompted invalid binding")
			}
		})
	}
}

func TestSendAssignmentHoldsTaskLockThroughPrompt(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	runner.onPrompt = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := service.store.Update(
			ctx,
			"task-1",
			func(task taskstate.Task) (taskstate.Task, error) {
				return task.ReserveLaunch("gen-2")
			},
		)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("restart during prompt = %v", err)
		}
	}
	if _, err := service.SendAssignment(context.Background(), root, "task-1", "brief", "Do work", herdr.Client{SessionRunner: runner}); err != nil {
		t.Fatal(err)
	}
}

func TestSendAssignmentPublicationFailureDoesNotReportDelivery(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "messages"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := service.SendAssignment(
		context.Background(),
		root,
		"task-1",
		"brief",
		"Do work",
		herdr.Client{SessionRunner: runner},
	)
	if err == nil || result.Message.ID.String() != "" || runner.calls != 0 {
		t.Fatalf("failed publication = %#v, %v", result, err)
	}
}

func TestSendAssignmentUnconfirmedPublicationRetriesSameMessage(t *testing.T) {
	service, runner, root := runningWorkerService(t)
	client := herdr.Client{SessionRunner: runner}
	worker, err := service.SupervisedWorker("task-1", root, client)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("publication synchronization unconfirmed")
	var published inbox.Message
	result, err := worker.sendAssignment(context.Background(), "brief", "Do work",
		func(recipient inbox.Recipient, key, body string) (inbox.Message, error) {
			message, err := worker.messages.Send(recipient, key, body)
			if err != nil {
				return message, err
			}
			published = message
			return message, failure
		})
	if !errors.Is(err, failure) || result != (AssignmentResult{}) || runner.calls != 0 {
		t.Fatalf("uncertain send = %#v, %v; calls = %d", result, err, runner.calls)
	}
	retry, err := service.SendAssignment(
		context.Background(),
		root,
		"task-1",
		"brief",
		"Do work",
		client,
	)
	if err != nil || retry.Message.ID != published.ID || retry.Notification != NotificationSent {
		t.Fatalf("retry = %#v, %v; published = %#v", retry, err, published)
	}
	pending, err := worker.messages.List(worker.recipient)
	if err != nil || len(pending) != 1 || pending[0].ID != published.ID {
		t.Fatalf("pending = %#v, %v", pending, err)
	}
}
