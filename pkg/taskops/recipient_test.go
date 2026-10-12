package taskops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestResolveUsesConfiguredPrimaryRecipient(t *testing.T) {
	root := testRoot(t)
	runningReplyTask(t, root)
	messageRoot := filepath.Join(t.TempDir(), "messages")
	original := sendBoundReply(t, root, messageRoot, "gen-1")
	recipient, err := inbox.ParseRecipient("supervisor-custom")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithPrimaryRecipient(root, recipient)
	if err != nil {
		t.Fatal(err)
	}
	messages, _ := inbox.New(messageRoot)
	reply, err := messages.Send(recipient, original.Key, original.Body)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		result, err := service.Resolve(
			context.Background(),
			messageRoot,
			reply.ID.String(),
			taskstate.Close,
			"2026-10-12",
		)
		if err != nil || result.State != taskstate.Done || result.Already != (attempt == 1) {
			t.Fatalf("configured resolution = %#v, %v", result, err)
		}
	}
	if _, err := messages.ReadHandled(recipient, reply.ID); err != nil {
		t.Fatalf("configured recipient not acknowledged: %v", err)
	}
	if _, err := messages.Read(original.Recipient, original.ID); err != nil {
		t.Fatalf("default mailbox changed: %v", err)
	}
	if _, err := messages.ReadDisposition(original.Recipient, original.ID); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("default disposition changed: %v", err)
	}
}

func TestNewWithPrimaryRecipientRejectsZeroRecipient(t *testing.T) {
	if _, err := NewWithPrimaryRecipient(testRoot(t), inbox.Recipient{}); !errors.Is(
		err,
		inbox.ErrInvalidRecipient,
	) {
		t.Fatalf("zero recipient = %v", err)
	}
}
