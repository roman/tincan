package tincancli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
)

func TestCompactInboxList(t *testing.T) {
	for _, count := range []int{0, 1, 22} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			root := t.TempDir()
			store, err := inbox.New(root)
			if err != nil {
				t.Fatal(err)
			}
			recipient, _ := inbox.ParseRecipient("worker")
			var rows strings.Builder
			for index := range count {
				key := fmt.Sprintf("task-assignment:/long/root:worker:generation:%d", index)
				message, err := store.Send(recipient, key, "assignment body")
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&rows, "  %s,\"task-assignment\"\n", message.ID)
			}
			var stdout, stderr bytes.Buffer
			args := []string{"--root", root, "inbox", "list", "worker"}
			if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
			want := fmt.Sprintf(
				"total: %d\nreturned: %d\ntruncated: false\nmessages[%d]{id,type}:\n%s",
				count,
				count,
				count,
				&rows,
			)
			if count == 0 {
				want = "total: 0\nreturned: 0\ntruncated: false\nmessages[0]:\n"
			}
			if !strings.HasPrefix(stdout.String(), want) {
				t.Fatalf("list=%s want prefix=%s", &stdout, want)
			}
			for _, hint := range []string{"tincan --root '" + root + "' inbox read worker MESSAGE_ID", "--full"} {
				if !strings.Contains(stdout.String(), hint) {
					t.Errorf("missing detail hint %q: %s", hint, &stdout)
				}
			}
			pending, err := store.List(recipient)
			if err != nil || len(pending) != count {
				t.Fatalf("list changed pending messages: count=%d err=%v", len(pending), err)
			}
		})
	}
}

func TestCompactInboxListKeyTypes(t *testing.T) {
	for _, trial := range []struct{ key, want string }{
		{"task-reply:/root:task:generation:hash", "task-reply"},
		{"", "message"},
		{"plain-retry-key", "message"},
		{":empty-prefix", "message"},
		{"a,\"b\n:context", "a,\"b\n"},
		{strings.Repeat("界", 40) + ":context", strings.Repeat("界", 32) + "…"},
	} {
		t.Run(trial.key, func(t *testing.T) {
			root := t.TempDir()
			store, err := inbox.New(root)
			if err != nil {
				t.Fatal(err)
			}
			recipient, _ := inbox.ParseRecipient("worker")
			message, err := store.Send(recipient, trial.key, "body")
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			args := []string{"--root", root, "inbox", "list", "worker"}
			if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stderr=%s", code, &stderr)
			}
			if want := fmt.Sprintf("  %s,%s\n", message.ID, quote(trial.want)); !strings.Contains(
				stdout.String(),
				want,
			) {
				t.Fatalf("list=%s want row=%q", &stdout, want)
			}
		})
	}
}
