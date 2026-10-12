package taskops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestCheckReplyReturnsOnlyValidatedCurrentContent(t *testing.T) {
	for _, mode := range []string{"handled assignment", "pending assignment", "unbound", "custom recipient"} {
		t.Run(mode, func(t *testing.T) {
			service, messages, assignment := assignmentFixture(t)
			input := replyInput(assignment)
			if mode == "unbound" {
				input.AssignmentID = ""
			}
			if mode == "custom recipient" {
				recipient, _ := inbox.ParseRecipient("custom-supervisor")
				var err error
				service, err = NewWithPrimaryRecipient(service.root, recipient)
				if err != nil {
					t.Fatal(err)
				}
			}
			delivery, err := service.SendReply(context.Background(), messages.Root(), input)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "pending assignment" {
				mailbox := filepath.Join(messages.Root(), "inbox", "task-1")
				filename := assignment.ID.String() + ".json"
				if err := os.Rename(filepath.Join(mailbox, "handled", filename), filepath.Join(mailbox, "pending", filename)); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.CheckReply(
				context.Background(),
				messages.Root(),
				delivery.Message.ID.String(),
			)
			if err != nil || result.State != ReplyCurrent || result.TaskID != input.TaskID ||
				result.Generation != input.Generation ||
				result.AssignmentID != input.AssignmentID ||
				result.Body != input.Body {
				t.Fatalf("check = %#v, %v", result, err)
			}
			if _, err := messages.Read(delivery.Message.Recipient, delivery.Message.ID); err != nil {
				t.Fatalf("check acknowledged reply: %v", err)
			}
		})
	}
}

func TestCheckReplySuppressesStaleContent(t *testing.T) {
	for _, state := range []string{"missing task", "new generation", "completed", "retired", "adoption", "generation", "launch"} {
		t.Run(state, func(t *testing.T) {
			service, messages, assignment := assignmentFixture(t)
			delivery, err := service.SendReply(
				context.Background(),
				messages.Root(),
				replyInput(assignment),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if state == "missing task" {
				if err := os.Remove(filepath.Join(service.root, "records", "task-1.json")); err != nil {
					t.Fatal(err)
				}
			} else if state == "completed" || state == "retired" {
				if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
					t.Fatal(err)
				}
				if state == "retired" {
					if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
						return task.RetireResult("gen-1")
					}); err != nil {
						t.Fatal(err)
					}
				}
			} else if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
				snapshot := task.Snapshot()
				switch state {
				case "new generation":
					snapshot.Worker.Generation = "gen-2"
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
			result, err := service.CheckReply(ctx, messages.Root(), delivery.Message.ID.String())
			if err != nil || result != (CheckedReply{State: ReplyStale, TaskID: "task-1"}) {
				t.Fatalf("stale check = %#v, %v", result, err)
			}
		})
	}
}

func TestCheckReplyReportsStorageAndLinkageFailures(t *testing.T) {
	for _, failure := range []string{"missing records", "corrupt task", "missing assignment", "invalid handled assignment", "wrong root", "wrong key"} {
		t.Run(failure, func(t *testing.T) {
			service, messages, assignment := assignmentFixture(t)
			delivery, err := service.SendReply(
				context.Background(),
				messages.Root(),
				replyInput(assignment),
			)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "missing records":
				if err := os.Rename(filepath.Join(service.root, "records"), filepath.Join(service.root, "records-saved")); err != nil {
					t.Fatal(err)
				}
			case "corrupt task":
				if err := os.WriteFile(filepath.Join(service.root, "records", "task-1.json"), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing assignment":
				if err := os.Remove(filepath.Join(messages.Root(), "inbox", "task-1", "handled", assignment.ID.String()+".json")); err != nil {
					t.Fatal(err)
				}
			case "invalid handled assignment":
				assignment.Body = "invalid"
				writeMessageFixture(t, messages.Root(), "handled", assignment)
			case "wrong root":
				var reply taskprotocol.Reply
				if err := json.Unmarshal([]byte(delivery.Message.Body), &reply); err != nil {
					t.Fatal(err)
				}
				reply.State = "/different/tasks"
				body, _ := json.Marshal(reply)
				delivery.Message.Body = string(body)
				writeMessageFixture(t, messages.Root(), "pending", delivery.Message)
			case "wrong key":
				delivery.Message.Key = "unrelated"
				writeMessageFixture(t, messages.Root(), "pending", delivery.Message)
			}
			result, err := service.CheckReply(
				context.Background(),
				messages.Root(),
				delivery.Message.ID.String(),
			)
			if err == nil || result != (CheckedReply{}) {
				t.Fatalf("failed check = %#v, %v", result, err)
			}
		})
	}
}

func writeMessageFixture(t *testing.T, root, location string, message inbox.Message) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(
		root,
		"inbox",
		message.Recipient.String(),
		location,
		message.ID.String()+".json",
	)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
