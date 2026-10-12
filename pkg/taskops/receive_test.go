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

func assignmentFixture(t *testing.T) (*Service, *inbox.Store, inbox.Message) {
	t.Helper()
	service, _, root := runningWorkerService(t)
	store, _ := inbox.New(root)
	recipient, _ := inbox.ParseRecipient("task-1")
	body, _ := json.Marshal(taskprotocol.Assignment{
		Kind: "task-assignment", State: filepath.Join(service.root, "tasks"),
		Task: "task-1", SpawnGeneration: "gen-1", Body: "Build the feature\nwith care",
	})
	message, err := store.Send(recipient, "assignment", string(body))
	if err != nil {
		t.Fatal(err)
	}
	return service, store, message
}

func TestReadAssignmentChecksBothGenerations(t *testing.T) {
	for _, test := range []struct {
		name, caller, worker string
		status               AssignmentState
		body                 string
	}{
		{"current", "gen-1", "gen-1", AssignmentCurrent, "Build the feature\nwith care"},
		{"old caller", "gen-old", "gen-1", AssignmentStale, ""},
		{"old message", "gen-2", "gen-2", AssignmentStale, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store, message := assignmentFixture(t)
			if test.worker != "gen-1" {
				if _, err := service.store.Update(context.Background(), "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.Worker.Generation = test.worker
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.ReadAssignment(
				context.Background(),
				store.Root(),
				"task-1",
				message.ID.String(),
				test.caller,
			)
			if err != nil || result.State != test.status || result.Body != test.body {
				t.Fatalf("read = %#v, %v", result, err)
			}
			if _, err := store.Read(message.Recipient, message.ID); err != nil {
				t.Fatalf("read changed message: %v", err)
			}
		})
	}
}

func TestAcknowledgeAssignmentPreservesStaleAndRetryStatuses(t *testing.T) {
	service, store, message := assignmentFixture(t)
	ctx := context.Background()
	if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		snapshot := task.Snapshot()
		snapshot.Worker.Generation = "gen-2"
		return taskstate.NewMigrated(snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		generation string
		state      AssignmentState
		pending    bool
	}{
		{"gen-1", AssignmentStale, true},
		{"gen-2", AssignmentStale, false},
		{"gen-1", AssignmentStale, false},
		{"gen-2", AssignmentAcknowledged, false},
	} {
		result, err := service.AcknowledgeAssignment(
			ctx,
			store.Root(),
			"task-1",
			message.ID.String(),
			test.generation,
		)
		if err != nil || result.State != test.state || result.Body != "" {
			t.Fatalf("ack %s = %#v, %v", test.generation, result, err)
		}
		_, err = store.Read(message.Recipient, message.ID)
		if (err == nil) != test.pending || (err != nil && !errors.Is(err, os.ErrNotExist)) {
			t.Fatalf("pending after ack %s = %v", test.generation, err)
		}
	}
	if err := taskprotocol.ValidateBoundAssignment(store, service.root, "task-1", "gen-1", message.ID); err != nil {
		t.Fatalf("handled assignment cannot bind a reply: %v", err)
	}
}

func TestReceiveAssignmentRejectsPendingAndCompletedWorkers(t *testing.T) {
	for _, flag := range []string{"adoption", "generation", "launch", "completed"} {
		for _, action := range []string{"read", "ack"} {
			t.Run(flag+"/"+action, func(t *testing.T) {
				service, store, message := assignmentFixture(t)
				ctx := context.Background()
				if flag == "completed" {
					if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
						t.Fatal(err)
					}
				} else if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					switch flag {
					case "adoption":
						snapshot.State = taskstate.Queued
						snapshot.Worker.PendingAdoption = true
						snapshot.Worker.Workspace, snapshot.Worker.Tab = "", ""
					case "generation":
						snapshot.Worker.PendingGeneration = "gen-2"
					case "launch":
						snapshot.Worker.PendingLaunch = "gen-2"
					}
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
				receive := service.ReadAssignment
				if action == "ack" {
					receive = service.AcknowledgeAssignment
				}
				result, err := receive(ctx, store.Root(), "task-1", message.ID.String(), "gen-1")
				if !errors.Is(err, taskstate.ErrInvalidTransition) ||
					result != (AssignmentStatus{}) {
					t.Fatalf("receive = %#v, %v", result, err)
				}
				if _, err := store.Read(message.Recipient, message.ID); err != nil {
					t.Fatalf("rejected receive changed message: %v", err)
				}
			})
		}
	}
}

func TestAcknowledgeAssignmentValidatesHandledEnvelopes(t *testing.T) {
	for _, test := range []string{"unrelated", "wrong root", "wrong task", "invalid generation"} {
		t.Run(test, func(t *testing.T) {
			service, store, original := assignmentFixture(t)
			envelope := taskprotocol.Assignment{
				Kind: "task-assignment", State: filepath.Join(service.root, "tasks"),
				Task: "task-1", SpawnGeneration: "gen-1", Body: "Work",
			}
			switch test {
			case "unrelated":
				envelope.Kind = "task-bootstrap"
			case "wrong root":
				envelope.State = "/different/tasks"
			case "wrong task":
				envelope.Task = "task-2"
			case "invalid generation":
				envelope.SpawnGeneration = ""
			}
			body, _ := json.Marshal(envelope)
			message, err := store.Send(original.Recipient, "bad", string(body))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Acknowledge(message.Recipient, message.ID); err != nil {
				t.Fatal(err)
			}
			result, err := service.AcknowledgeAssignment(
				context.Background(),
				store.Root(),
				"task-1",
				message.ID.String(),
				"gen-1",
			)
			if err == nil || result != (AssignmentStatus{}) {
				t.Fatalf("handled invalid ack = %#v, %v", result, err)
			}
		})
	}
}

func TestReceiveAssignmentOldCallerDoesNotAccessMailbox(t *testing.T) {
	service, store, message := assignmentFixture(t)
	backup := store.Root() + "-saved"
	if err := os.Rename(store.Root(), backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Root(), []byte("not a mailbox"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, receive := range []func(context.Context, string, string, string, string) (AssignmentStatus, error){
		service.ReadAssignment, service.AcknowledgeAssignment,
	} {
		result, err := receive(
			context.Background(),
			store.Root(),
			"task-1",
			message.ID.String(),
			"gen-old",
		)
		if err != nil || result.State != AssignmentStale || result.Body != "" {
			t.Fatalf("old caller = %#v, %v", result, err)
		}
	}
}

func TestAcknowledgeAssignmentRetriesAfterFilesystemFailure(t *testing.T) {
	service, store, message := assignmentFixture(t)
	handled := filepath.Join(store.Root(), "inbox", "task-1", "handled")
	if err := os.Rename(handled, handled+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handled, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := service.AcknowledgeAssignment(
		context.Background(),
		store.Root(),
		"task-1",
		message.ID.String(),
		"gen-1",
	)
	if err == nil || result != (AssignmentStatus{}) {
		t.Fatalf("failed ack = %#v, %v", result, err)
	}
	if err := os.Remove(handled); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(handled+"-saved", handled); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := service.AcknowledgeAssignment(
			context.Background(),
			store.Root(),
			"task-1",
			message.ID.String(),
			"gen-1",
		)
		if err != nil || result.State != AssignmentAcknowledged || result.Body != "" {
			t.Fatalf("ack retry = %#v, %v", result, err)
		}
	}
}
