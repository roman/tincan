package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
)

func TestWaitExitCodeContract(t *testing.T) {
	for _, test := range []struct {
		name     string
		pending  bool
		timeout  string
		wantCode int
		wantLine string
	}{
		{name: "pending", pending: true, timeout: "10ms", wantLine: "status: pending"},
		{name: "checkpoint", timeout: "10ms", wantLine: "status: checkpoint"},
		{name: "invalid timeout", timeout: "0s", wantCode: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.pending {
				store, err := inbox.New(root)
				if err != nil {
					t.Fatal(err)
				}
				recipient, err := inbox.ParseRecipient("firstmate")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Send(recipient, "reply-1", "Worker reply"); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(os.Args[0], "-test.run=^TestInboxCommandHelper$", "--",
				"--root", root, "wait", "firstmate", "--timeout", test.timeout,
				"--interval", "1ms")
			command.Env = append(os.Environ(), "TINCAN_INBOX_HELPER=1")
			output, err := command.CombinedOutput()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != test.wantCode {
				t.Fatalf("exit code = %d, output = %q; want %d", code, output, test.wantCode)
			}
			if test.wantLine != "" {
				captured := strings.TrimRight(string(output), "\n")
				if !strings.HasPrefix(captured, test.wantLine+"\n") {
					t.Fatalf("hook-visible output = %q; want prefix %q", captured, test.wantLine+"\n")
				}
			}
		})
	}
}

func TestInboxCommandHelper(t *testing.T) {
	if os.Getenv("TINCAN_INBOX_HELPER") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		t.Fatal("missing helper argument separator")
	}
	os.Args = append([]string{"tincan-inbox"}, os.Args[separator+1:]...)
	main()
}

func TestSendRequiresExplicitBodySource(t *testing.T) {
	root := t.TempDir()
	command := &CLI{Root: root}
	command.Send.Recipient = "worker"
	if err := run(context.Background(), command, "send <recipient>"); err == nil {
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
