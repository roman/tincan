package taskops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func replyInput(message inbox.Message) ReplyInput {
	return ReplyInput{TaskID: "task-1", Generation: "gen-1", Key: "answer",
		AssignmentID: message.ID.String(), Body: "Finished\nwith tests"}
}

func TestSendReplyConfirmsDeliveryBeforeAcknowledgement(t *testing.T) {
	for _, mode := range []string{"pending assignment", "handled assignment", "unbound", "custom recipient"} {
		t.Run(mode, func(t *testing.T) {
			service, messages, assignment := assignmentFixture(t)
			input := replyInput(assignment)
			if mode == "handled assignment" {
				if err := messages.Acknowledge(assignment.Recipient, assignment.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unbound" {
				input.AssignmentID = ""
			}
			primary, _ := inbox.ParseRecipient(DefaultPrimaryRecipient)
			if mode == "custom recipient" {
				primary, _ = inbox.ParseRecipient("custom-supervisor")
				var err error
				service, err = NewWithPrimaryRecipient(service.root, primary)
				if err != nil {
					t.Fatal(err)
				}
			}
			var first inbox.Message
			for attempt := range 2 {
				result, err := service.SendReply(context.Background(), messages.Root(), input)
				wantAcknowledgement := ReplyAcknowledged
				if mode == "unbound" {
					wantAcknowledgement = ReplyUnbound
				}
				if err != nil || result.Assignment != wantAcknowledgement ||
					result.Message.Recipient != primary {
					t.Fatalf("delivery = %#v, %v", result, err)
				}
				if attempt == 0 {
					first = result.Message
				} else if result.Message.ID != first.ID {
					t.Fatal("reply retry changed identity")
				}
				envelope, err := taskprotocol.DecodeReply(
					result.Message.Body,
					result.Message.Key,
					service.root,
				)
				if err != nil || envelope.Body != input.Body ||
					envelope.AssignmentID != input.AssignmentID ||
					envelope.SpawnGeneration != input.Generation {
					t.Fatalf("reply envelope = %#v, %v", envelope, err)
				}
				keySuffix := input.AssignmentID
				if mode == "unbound" {
					keySuffix = input.Key
					if _, err := messages.Read(assignment.Recipient, assignment.ID); err != nil {
						t.Fatalf("unbound reply acknowledged assignment: %v", err)
					}
				} else if _, err := messages.ReadHandled(assignment.Recipient, assignment.ID); err != nil {
					t.Fatalf("assignment not acknowledged: %v", err)
				}
				if result.Message.Key != fmt.Sprintf(
					"task-reply:%s:task-1:gen-1:%s",
					service.root,
					keySuffix,
				) {
					t.Fatalf("reply key = %q", result.Message.Key)
				}
			}
		})
	}
}

func TestSendReplyRepairsAcknowledgementAfterResultAndNewWorker(t *testing.T) {
	for _, state := range []string{"completed", "retired", "new worker", "pending launch", "handled reply"} {
		t.Run(state, func(t *testing.T) {
			service, messages, assignment := assignmentFixture(t)
			input := replyInput(assignment)
			handled := filepath.Join(messages.Root(), "inbox", "task-1", "handled")
			if err := os.Rename(handled, handled+"-saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(handled, []byte("blocked"), 0600); err != nil {
				t.Fatal(err)
			}
			first, err := service.SendReply(context.Background(), messages.Root(), input)
			if err == nil || first.Message.ID.String() == "" ||
				first.Assignment != ReplyAcknowledgementUnconfirmed {
				t.Fatalf("interrupted acknowledgement = %#v, %v", first, err)
			}
			if err := os.Remove(handled); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(handled+"-saved", handled); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if state == "pending launch" {
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					return task.ReserveLaunch("gen-2")
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				mode, closedOn := taskstate.Close, "2026-10-12"
				if state == "new worker" {
					mode, closedOn = taskstate.Retain, ""
					if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
						return task.PlaceHold(taskstate.Hold{Reason: "Another pass", Kind: "captain"})
					}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := service.Finish(ctx, "task-1", "gen-1", mode, closedOn); err != nil {
					t.Fatal(err)
				}
				if state == "retired" || state == "new worker" {
					if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
						return task.RetireResult("gen-1")
					}); err != nil {
						t.Fatal(err)
					}
				}
				if state == "new worker" {
					if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
						return task.ReleaseHold()
					}); err != nil {
						t.Fatal(err)
					}
					adoptStoredWorker(t, service.store, "task-1", taskstate.Worker{
						Name: "worker-2", Harness: "codex", Session: "work", Pane: "pane-2", Project: service.root,
						Worktree: t.TempDir(), Generation: "gen-2", AgentArgs: []string{"resume", "thread-2"},
					}, "tab-2")
					if _, err := service.Start(ctx, "task-1"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if state == "handled reply" {
				if err := messages.Acknowledge(first.Message.Recipient, first.Message.ID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			retried, err := service.SendReply(ctx, messages.Root(), input)
			if err != nil || retried.Message.ID != first.Message.ID ||
				retried.Assignment != ReplyAcknowledged {
				t.Fatalf("recovery = %#v, %v", retried, err)
			}
			after, err := service.store.Read(ctx, "task-1")
			if err != nil || !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatalf("recovery changed task = %#v, %v", after.Snapshot(), err)
			}
			if state == "handled reply" {
				if _, err := messages.Read(first.Message.Recipient, first.Message.ID); !errors.Is(
					err,
					os.ErrNotExist,
				) {
					t.Fatalf("recovery reopened reply: %v", err)
				}
			}
		})
	}
}

func TestSendReplyRecoveryCannotCreateOrChangeReply(t *testing.T) {
	for _, change := range []string{"absent", "body", "key", "recipient", "assignment"} {
		t.Run(change, func(t *testing.T) {
			service, messages, assignment := assignmentFixture(t)
			input := replyInput(assignment)
			if change != "absent" {
				if _, err := service.SendReply(context.Background(), messages.Root(), input); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.Finish(context.Background(), "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "body":
				input.Body = "Different"
			case "key":
				input.Key = "different"
			case "recipient":
				recipient, _ := inbox.ParseRecipient("different")
				var err error
				service, err = NewWithPrimaryRecipient(service.root, recipient)
				if err != nil {
					t.Fatal(err)
				}
			case "assignment":
				other, err := messages.Send(
					assignment.Recipient,
					"other-assignment",
					assignment.Body,
				)
				if err != nil {
					t.Fatal(err)
				}
				input.AssignmentID = other.ID.String()
			}
			result, err := service.SendReply(context.Background(), messages.Root(), input)
			if err == nil || result != (ReplyDelivery{}) {
				t.Fatalf("invalid recovery = %#v, %v", result, err)
			}
		})
	}
}

func TestSendReplyPublicationFailureLeavesAssignmentPending(t *testing.T) {
	service, messages, assignment := assignmentFixture(t)
	primary := filepath.Join(messages.Root(), "inbox", DefaultPrimaryRecipient)
	if err := os.WriteFile(primary, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := service.SendReply(context.Background(), messages.Root(), replyInput(assignment))
	if err == nil || result != (ReplyDelivery{}) {
		t.Fatalf("failed publication = %#v, %v", result, err)
	}
	if _, err := messages.Read(assignment.Recipient, assignment.ID); err != nil {
		t.Fatalf("failed publication acknowledged assignment: %v", err)
	}
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	result, err = service.SendReply(context.Background(), messages.Root(), replyInput(assignment))
	if err != nil || result.Assignment != ReplyAcknowledged {
		t.Fatalf("publication retry = %#v, %v", result, err)
	}
}

func TestSendReplyRejectsInvalidInputAndAssignment(t *testing.T) {
	for _, change := range []string{"blank body", "blank key", "invalid generation", "wrong generation", "wrong assignment"} {
		t.Run(change, func(t *testing.T) {
			service, messages, assignment := assignmentFixture(t)
			input := replyInput(assignment)
			switch change {
			case "blank body":
				input.Body = " \n"
			case "blank key":
				input.Key = " \n"
			case "invalid generation":
				input.Generation = ""
			case "wrong generation":
				input.Generation = "gen-old"
			case "wrong assignment":
				other, err := messages.Send(assignment.Recipient, "unrelated", "Unrelated")
				if err != nil {
					t.Fatal(err)
				}
				input.AssignmentID = other.ID.String()
			}
			result, err := service.SendReply(context.Background(), messages.Root(), input)
			if err == nil || result != (ReplyDelivery{}) {
				t.Fatalf("invalid send = %#v, %v", result, err)
			}
			primary, _ := inbox.ParseRecipient(DefaultPrimaryRecipient)
			pending, err := messages.List(primary)
			if err != nil || len(pending) != 0 {
				t.Fatalf("invalid reply published: %#v, %v", pending, err)
			}
			if _, err := messages.Read(assignment.Recipient, assignment.ID); err != nil {
				t.Fatalf("invalid reply acknowledged assignment: %v", err)
			}
		})
	}
}
