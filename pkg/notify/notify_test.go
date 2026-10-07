package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
)

type fakeStore struct {
	messages []inbox.Message
	err      error
}

func (store fakeStore) List(inbox.Recipient) ([]inbox.Message, error) {
	return store.messages, store.err
}

type fakeDoorbell struct {
	prompts []string
	err     error
}

func (bell *fakeDoorbell) Prompt(_ context.Context, _ string, message string) error {
	bell.prompts = append(bell.prompts, message)
	return bell.err
}

func TestNotifyOnceRingsOnlyForPendingMessages(t *testing.T) {
	tests := []struct {
		name        string
		messages    []inbox.Message
		listErr     error
		promptErr   error
		wantPrompts int
		wantErr     error
	}{
		{name: "empty"},
		{name: "one message", messages: []inbox.Message{{}}, wantPrompts: 1},
		{name: "several messages one ring", messages: []inbox.Message{{}, {}}, wantPrompts: 1},
		{name: "store failure", listErr: errors.New("read failed"), wantErr: ErrList},
		{name: "doorbell failure", messages: []inbox.Message{{}}, promptErr: errors.New("send failed"), wantPrompts: 1, wantErr: ErrPrompt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bell := &fakeDoorbell{err: tt.promptErr}
			err := NotifyOnce(context.Background(), fakeStore{messages: tt.messages, err: tt.listErr}, bell, mustRecipient("worker"))
			if len(bell.prompts) != tt.wantPrompts {
				t.Fatalf("prompts = %d, want %d", len(bell.prompts), tt.wantPrompts)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			for _, prompt := range bell.prompts {
				if prompt != DoorbellText {
					t.Fatalf("prompt includes message content: %q", prompt)
				}
			}
		})
	}
}

type changingStore struct {
	calls       int
	reachedFour chan struct{}
}

func (store *changingStore) List(inbox.Recipient) ([]inbox.Message, error) {
	store.calls++
	if store.calls == 4 {
		close(store.reachedFour)
	}
	if store.calls <= 2 {
		return []inbox.Message{{}}, nil
	}
	return nil, nil
}

func TestRunRetriesUntilAcknowledged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &changingStore{reachedFour: make(chan struct{})}
	bell := &fakeDoorbell{}
	go func() {
		<-store.reachedFour
		cancel()
	}()
	err := Run(ctx, store, bell, mustRecipient("worker"), time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bell.prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(bell.prompts))
	}
}

func TestRunRejectsInvalidRecipient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	store, storeErr := inbox.New(t.TempDir())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	err := Run(ctx, store, &fakeDoorbell{}, inbox.Recipient{}, time.Millisecond, nil)
	if !errors.Is(err, inbox.ErrInvalidRecipient) {
		t.Fatalf("error = %v, want invalid recipient", err)
	}
}

func mustRecipient(raw string) inbox.Recipient {
	recipient, err := inbox.ParseRecipient(raw)
	if err != nil {
		panic(err)
	}
	return recipient
}
