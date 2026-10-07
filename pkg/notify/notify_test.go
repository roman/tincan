package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
)

type fakeStore struct {
	messages      []inbox.Message
	err           error
	seenRecipient *inbox.Recipient
}

func (store fakeStore) List(recipient inbox.Recipient) ([]inbox.Message, error) {
	if store.seenRecipient != nil {
		*store.seenRecipient = recipient
	}
	return store.messages, store.err
}

type fakeDoorbell struct {
	prompts []string
	targets []string
	err     error
}

func (bell *fakeDoorbell) Prompt(_ context.Context, target, message string) error {
	bell.targets = append(bell.targets, target)
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
		{
			name:        "doorbell failure",
			messages:    []inbox.Message{{}},
			promptErr:   errors.New("send failed"),
			wantPrompts: 1,
			wantErr:     ErrPrompt,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bell := &fakeDoorbell{err: tt.promptErr}
			var seenRecipient inbox.Recipient
			notifier := Notifier{
				Store: fakeStore{
					messages:      tt.messages,
					err:           tt.listErr,
					seenRecipient: &seenRecipient,
				},
				Doorbell:  bell,
				Recipient: mustRecipient("worker"),
				Target:    mustTarget("w1:p2"),
			}
			err := notifier.NotifyOnce(context.Background())
			if seenRecipient != mustRecipient("worker") {
				t.Fatalf("inbox recipient = %q, want worker", seenRecipient)
			}
			if len(bell.prompts) != tt.wantPrompts {
				t.Fatalf("prompts = %d, want %d", len(bell.prompts), tt.wantPrompts)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			for _, target := range bell.targets {
				if target != "w1:p2" {
					t.Fatalf("doorbell target = %q, want w1:p2", target)
				}
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
	notifier := Notifier{
		Store:     store,
		Doorbell:  bell,
		Recipient: mustRecipient("worker"),
		Target:    mustTarget("w1:p2"),
	}
	err := notifier.Run(ctx, time.Millisecond, nil)
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
	notifier := Notifier{
		Store:    store,
		Doorbell: &fakeDoorbell{},
		Target:   mustTarget("w1:p2"),
	}
	err := notifier.Run(ctx, time.Millisecond, nil)
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

func TestNotifyOnceRejectsZeroTarget(t *testing.T) {
	bell := &fakeDoorbell{}
	notifier := Notifier{Store: fakeStore{}, Doorbell: bell, Recipient: mustRecipient("worker")}
	err := notifier.NotifyOnce(context.Background())
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("NotifyOnce error = %v, want invalid target", err)
	}
	if len(bell.prompts) != 0 {
		t.Fatalf("prompted invalid target: %v", bell.targets)
	}
}

func TestRunRejectsZeroTargetImmediately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	store := fakeStore{err: errors.New("store should not be called")}
	bell := &fakeDoorbell{}
	notifier := Notifier{Store: store, Doorbell: bell, Recipient: mustRecipient("worker")}
	err := notifier.Run(ctx, time.Millisecond, nil)
	if !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("Run error = %v, want invalid target", err)
	}
	if len(bell.prompts) != 0 {
		t.Fatalf("prompted invalid target: %v", bell.targets)
	}
}

func mustTarget(raw string) Target {
	target, err := ParseTarget(raw)
	if err != nil {
		panic(err)
	}
	return target
}
