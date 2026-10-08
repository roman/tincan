package inbox

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestSendReadAndAcknowledge(t *testing.T) {
	store := newTestStore(t)
	sent, err := store.Send(mustRecipient(t, "worker-1"), "request-7", "Do the work")
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(mustRecipient(t, "worker-1"), sent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "Do the work" || got.Key != "request-7" || got.Recipient.String() != "worker-1" {
		t.Fatalf("unexpected message: %+v", got)
	}
	listed, err := store.List(mustRecipient(t, "worker-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != sent.ID {
		t.Fatalf("unexpected list: %+v", listed)
	}
	if err := store.Acknowledge(mustRecipient(t, "worker-1"), sent.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Acknowledge(mustRecipient(t, "worker-1"), sent.ID); err != nil {
		t.Fatalf("acknowledgement should be idempotent: %v", err)
	}
	listed, err = store.List(mustRecipient(t, "worker-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("acknowledged message remains pending: %+v", listed)
	}
	_, readErr := store.Read(mustRecipient(t, "worker-1"), sent.ID)
	if !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("read after acknowledgement: %v", readErr)
	}
}

func TestSendBodyContract(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		body      string
		seedBody  string
		wantBlank bool
	}{
		{name: "empty", wantBlank: true},
		{name: "whitespace only", key: "retry", body: " \t\n", wantBlank: true},
		{name: "blank retry key", key: "retry", seedBody: "original", body: " \n", wantBlank: true},
		{name: "preserve whitespace and newlines", body: "  first\nsecond \t"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			recipient := mustRecipient(t, "worker")
			if test.seedBody != "" {
				if _, err := store.Send(recipient, test.key, test.seedBody); err != nil {
					t.Fatal(err)
				}
			}
			message, err := store.Send(recipient, test.key, test.body)
			if test.wantBlank {
				if !errors.Is(err, ErrBlankBody) {
					t.Fatalf("Send error = %v, want blank body", err)
				}
				if test.seedBody == "" {
					entries, readErr := os.ReadDir(store.Root())
					if readErr != nil {
						t.Fatal(readErr)
					}
					if len(entries) != 0 {
						t.Fatalf("blank send created files: %v", entries)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			stored, err := store.Read(recipient, message.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Body != test.body {
				t.Fatalf("stored body = %q, want %q", stored.Body, test.body)
			}
		})
	}
}

func TestSendWithKeyIsIdempotentAcrossAcknowledgement(t *testing.T) {
	store := newTestStore(t)
	first, err := store.Send(mustRecipient(t, "worker"), "operation-1", "body")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Send(mustRecipient(t, "worker"), "operation-1", "body")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate ID: %s != %s", second.ID, first.ID)
	}
	if err := store.Acknowledge(mustRecipient(t, "worker"), first.ID); err != nil {
		t.Fatal(err)
	}
	third, err := store.Send(mustRecipient(t, "worker"), "operation-1", "body")
	if err != nil {
		t.Fatal(err)
	}
	if third.ID != first.ID {
		t.Fatalf("duplicate after acknowledgement: %s != %s", third.ID, first.ID)
	}
	pending, err := store.List(mustRecipient(t, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("duplicate restored pending message: %+v", pending)
	}
	_, sendErr := store.Send(mustRecipient(t, "worker"), "operation-1", "different")
	if !errors.Is(sendErr, ErrKeyConflict) {
		t.Fatalf("key reuse with different body: %v", sendErr)
	}
}

func TestConcurrentSendWithSameKey(t *testing.T) {
	store := newTestStore(t)
	recipient := mustRecipient(t, "worker")
	const count = 20
	ids := make(chan MessageID, count)
	failures := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			message, err := store.Send(recipient, "same", "body")
			if err != nil {
				failures <- err
				return
			}
			ids <- message.ID
		}()
	}
	group.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var first MessageID
	for id := range ids {
		if first.String() == "" {
			first = id
		}
		if id != first {
			t.Fatalf("different IDs: %s and %s", first, id)
		}
	}
	pending, err := store.List(mustRecipient(t, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one pending message, got %d", len(pending))
	}
}

func TestReadAndListRejectForgedMessageFields(t *testing.T) {
	for _, test := range []struct {
		name  string
		forge func(*Message, Recipient, MessageID)
	}{
		{name: "recipient", forge: func(message *Message, _ Recipient, _ MessageID) {
			message.Recipient = mustRecipient(t, "another-worker")
		}},
		{name: "ID", forge: func(message *Message, _ Recipient, _ MessageID) {
			other, err := generateMessageID("other-key")
			if err != nil {
				t.Fatal(err)
			}
			message.ID = other
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			recipient := mustRecipient(t, "worker")
			sent, err := store.Send(recipient, "key", "body")
			if err != nil {
				t.Fatal(err)
			}
			forged := sent
			test.forge(&forged, recipient, sent.ID)
			data, err := json.Marshal(forged)
			if err != nil {
				t.Fatal(err)
			}
			path := store.mailboxPath(recipient).pendingPath().messagePath(sent.ID)
			if err := os.WriteFile(path.String(), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Read(recipient, sent.ID); !errors.Is(err, ErrCorruptMessage) {
				t.Fatalf("Read error = %v", err)
			}
			if _, err := store.List(recipient); !errors.Is(err, ErrCorruptMessage) {
				t.Fatalf("List error = %v", err)
			}
		})
	}
}

func TestAcknowledgeSyncErrorNamesDirectory(t *testing.T) {
	for _, test := range []struct {
		name    string
		missing string
	}{
		{name: "handled directory", missing: "handled"},
		{name: "pending directory", missing: "pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := newRootPath(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			mailbox := root.mailboxPath(mustRecipient(t, "worker"))
			handled, pending := mailbox.handledPath(), mailbox.pendingPath()
			missingDirectory := pending
			if test.missing == "handled" {
				missingDirectory = handled
			}
			for _, directory := range []messageDirectoryPath{handled, pending} {
				if directory == missingDirectory {
					continue
				}
				if err := os.MkdirAll(directory.String(), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err = syncAcknowledgeDirectories(mailbox)
			if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), test.missing) {
				t.Fatalf("sync error = %v; want %s directory context", err, test.missing)
			}
		})
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func mustRecipient(t *testing.T, raw string) Recipient {
	t.Helper()
	value, err := ParseRecipient(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
