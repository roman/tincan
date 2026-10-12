package tincancli

import (
	"context"
	"errors"
	"github.com/roman/tincan/pkg/inbox"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSendRequiresExplicitBodySource(t *testing.T) {
	root := t.TempDir()
	command := &InboxCommands{}
	command.Send.Recipient = "worker"
	if err := runInbox(context.Background(), root, command, "send <recipient>", os.Stdin, io.Discard); err == nil {
		t.Fatal("send without a body source succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "inbox")); !os.IsNotExist(err) {
		t.Fatalf("inbox was created: %v", err)
	}
}

func TestWaitReturnsForMessageArrivingAfterStart(t *testing.T) {
	store, err := inbox.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, sendErr := store.Send(recipient, "reply-1", "Worker reply")
		sent <- sendErr
	}()
	count, err := waitForPending(context.Background(), store, recipient,
		time.Second, 5*time.Millisecond)
	if err != nil || count != 1 {
		t.Fatalf("wait count=%d err=%v", count, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

func TestWaitReturnsCheckpointForEmptyInbox(t *testing.T) {
	store, err := inbox.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	count, err := waitForPending(context.Background(), store, recipient,
		30*time.Millisecond, 5*time.Millisecond)
	if err != nil || count != 0 {
		t.Fatalf("checkpoint count=%d err=%v", count, err)
	}
}

func TestWaitRejectsInvalidDurations(t *testing.T) {
	store, err := inbox.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		timeout  time.Duration
		interval time.Duration
	}{
		{name: "zero timeout", interval: time.Millisecond},
		{name: "negative timeout", timeout: -time.Second, interval: time.Millisecond},
		{name: "zero interval", timeout: time.Second},
		{name: "negative interval", timeout: time.Second, interval: -time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := waitForPending(context.Background(), store, recipient,
				test.timeout, test.interval); err == nil {
				t.Fatal("invalid duration accepted")
			}
		})
	}
}

func TestWaitReturnsCanceledContext(t *testing.T) {
	store, err := inbox.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 16 {
		if _, err := waitForPending(ctx, store, recipient,
			time.Second, time.Millisecond); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled wait returned %v", err)
		}
	}
}
