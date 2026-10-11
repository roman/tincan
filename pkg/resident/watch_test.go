package resident

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
)

type recordingDoorbell struct{ calls chan string }

func (bell recordingDoorbell) Prompt(_ context.Context, target, _ string) error {
	bell.calls <- target
	return nil
}

func TestMessageEventWakesWorkerAcrossControllerRestart(t *testing.T) {
	root := t.TempDir()
	store, err := inbox.New(root)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("task-1")
	if err != nil {
		t.Fatal(err)
	}
	bell := recordingDoorbell{calls: make(chan string, 2)}
	start := func() (context.CancelFunc, <-chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		ready := make(chan struct{})
		ended := make(chan error, 1)
		go func() {
			ended <- (Watcher{
				Store: store, Recipient: recipient, Target: "pane-1", Doorbell: bell,
			}).Run(ctx, ready)
		}()
		select {
		case <-ready:
		case err := <-ended:
			t.Fatalf("watcher stopped before ready: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("watcher did not start")
		}
		return cancel, ended
	}
	for index, key := range []string{"first", "after-restart"} {
		var message inbox.Message
		if index == 1 {
			message, err = store.Send(recipient, key, "Read this task")
			if err != nil {
				t.Fatal(err)
			}
		}
		cancel, ended := start()
		if index == 0 {
			message, err = store.Send(recipient, key, "Read this task")
			if err != nil {
				t.Fatal(err)
			}
		}
		select {
		case target := <-bell.calls:
			if target != "pane-1" {
				t.Fatalf("target=%q", target)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("message event did not wake worker")
		}
		if err := store.Acknowledge(recipient, message.ID); err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case err := <-ended:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("watcher did not stop")
		}
	}
}

type failingDoorbell struct {
	attempts atomic.Int32
	calls    chan struct{}
}

func (bell *failingDoorbell) Prompt(context.Context, string, string) error {
	count := bell.attempts.Add(1)
	bell.calls <- struct{}{}
	if count == 1 {
		return errors.New("temporary Herdr failure")
	}
	return nil
}

func TestWatcherRetriesFailedDoorbell(t *testing.T) {
	store, err := inbox.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	if _, err := store.Send(recipient, "retry", "Read this task"); err != nil {
		t.Fatal(err)
	}
	bell := &failingDoorbell{calls: make(chan struct{}, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() {
		ended <- (Watcher{
			Store: store, Recipient: recipient, Target: "pane-1", Doorbell: bell,
			RetryDelay: 20 * time.Millisecond,
		}).Run(ctx, nil)
	}()
	for range 2 {
		select {
		case <-bell.calls:
		case <-time.After(2 * time.Second):
			t.Fatal("doorbell was not retried")
		}
	}
	cancel()
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
}

type cancelingDoorbell struct{}

func (cancelingDoorbell) Prompt(ctx context.Context, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestWatcherCancelsDuringDoorbell(t *testing.T) {
	store, err := inbox.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	if _, err := store.Send(recipient, "cancel", "Read this task"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := (Watcher{
		Store: store, Recipient: recipient, Target: "pane-1", Doorbell: cancelingDoorbell{},
	}).Run(ctx, nil); err != nil {
		t.Fatalf("cancellation = %v", err)
	}
}
