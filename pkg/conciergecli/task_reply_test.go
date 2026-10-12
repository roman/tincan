package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestTaskReplySendCheckAndRetiredRetry(t *testing.T) {
	t.Setenv(workerLaunchGeneration, "gen-1")
	for _, binding := range []string{"bound", "unbound"} {
		t.Run(binding, func(t *testing.T) {
			root := lifecycleRoot(t)
			invoke(t, root, "", "backlog", "start", "task-1")
			messageRoot := t.TempDir()
			assignment := assignmentMessage(t, root, messageRoot, "gen-1", "Work")
			body := "Complete result\n\"quoted\" ✓\n"
			path := filepath.Join(t.TempDir(), "result")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{
				"--message-root",
				messageRoot,
				"--recipient",
				"operator",
				"task",
				"reply",
				"send",
				"task-1",
				"--generation",
				"gen-1",
				"--key",
				"answer",
				"--body-file",
				path,
			}
			assignmentStatus := "not_requested"
			if binding == "bound" {
				args = append(args, "--assignment-id", assignment.ID.String())
				assignmentStatus = "acknowledged"
			}
			got := invoke(t, root, "", args...)
			messages, _ := inbox.New(messageRoot)
			selected, _ := inbox.ParseRecipient("operator")
			other, _ := inbox.ParseRecipient("concierge")
			replies, err := messages.List(selected)
			if err != nil || len(replies) != 1 {
				t.Fatalf("selected reply: %v %v", replies, err)
			}
			id := replies[0].ID.String()
			want := "message_id: \"" + id + "\"\nassignment: " + assignmentStatus + "\n"
			if got != want {
				t.Fatalf("send: %s", got)
			}
			if replies, err := messages.List(other); err != nil || len(replies) != 0 {
				t.Fatalf("wrong primary: %v %v", replies, err)
			}
			check := []string{
				"--message-root",
				messageRoot,
				"--recipient",
				"operator",
				"task",
				"reply",
				"check",
				id,
			}
			expected := "status: current\ntask: \"task-1\"\ngeneration: \"gen-1\"\n"
			if binding == "bound" {
				expected += "assignment_id: \"" + assignment.ID.String() + "\"\n"
			}
			expected += "body: \"Complete result\\n\\\"quoted\\\" ✓\\n\"\n"
			launch := ""
			if binding == "unbound" {
				launch = "invalid worker context"
			}
			t.Setenv(workerLaunchGeneration, launch)
			if got := invoke(t, root, "", check...); got != expected {
				t.Fatalf("check: %s", got)
			}
			t.Setenv(workerLaunchGeneration, "gen-1")
			invoke(
				t,
				root,
				"",
				"backlog",
				"finish",
				"task-1",
				"--generation",
				"gen-1",
				"--closed-on",
				"2026-10-12",
			)
			store, _ := taskstate.NewStore(root)
			if _, err := store.Update(context.Background(), "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.RetireResult("gen-1") }); err != nil {
				t.Fatal(err)
			}
			if got := invoke(t, root, "", check...); got != "status: stale\ntask: \"task-1\"\n" {
				t.Fatalf("stale content: %s", got)
			}
			for i := range args {
				if args[i] == path {
					args[i] = "-"
				}
			}
			if got := invoke(t, root, body, args...); got != want {
				t.Fatalf("retired retry: %s", got)
			}
		})
	}
}

func TestTaskReplyPreservesDeliveryWhenAcknowledgementFails(t *testing.T) {
	t.Setenv(workerLaunchGeneration, "gen-1")
	root := lifecycleRoot(t)
	invoke(t, root, "", "backlog", "start", "task-1")
	messageRoot := t.TempDir()
	assignment := assignmentMessage(t, root, messageRoot, "gen-1", "Work")
	handled := filepath.Join(messageRoot, "inbox", "task-1", "handled")
	if err := os.Rename(handled, handled+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handled, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--message-root",
		messageRoot,
		"task",
		"reply",
		"send",
		"task-1",
		"--generation",
		"gen-1",
		"--key",
		"answer",
		"--assignment-id",
		assignment.ID.String(),
		"--body-file",
		"-",
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader("Done"), &stdout, &stderr); code != 1 ||
		!strings.Contains(stdout.String(), "assignment: unconfirmed\n") ||
		stderr.Len() != 0 {
		t.Fatalf("ack error: %d %s %s", code, &stdout, &stderr)
	}
	messages, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("concierge")
	replies, err := messages.List(recipient)
	if err != nil || len(replies) != 1 ||
		!strings.HasPrefix(stdout.String(), "message_id: \""+replies[0].ID.String()+"\"\n") {
		t.Fatalf("lost confirmed reply: %v %v %s", replies, err, &stdout)
	}
	if err := os.Remove(handled); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(handled+"-saved", handled); err != nil {
		t.Fatal(err)
	}
	if got := invoke(t, root, "Done", args...); got != "message_id: \""+replies[0].ID.String()+"\"\nassignment: acknowledged\n" {
		t.Fatalf("repair: %s", got)
	}
	if _, err := messages.ReadHandled(assignment.Recipient, assignment.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTaskReplyUsagePublicationAndWriterFailures(t *testing.T) {
	t.Setenv(workerLaunchGeneration, "gen-1")
	root := lifecycleRoot(t)
	invoke(t, root, "", "backlog", "start", "task-1")
	messageRoot := t.TempDir()
	args := []string{
		"--message-root",
		messageRoot,
		"task",
		"reply",
		"send",
		"task-1",
		"--generation",
		"gen-1",
		"--key",
		"answer",
		"--body-file",
		"-",
	}
	expectLifecycleError(t, root, 2, args[:len(args)-2]...)
	expectLifecycleError(t, root, 2, append(args, "--recipient", "../bad")...)
	if err := os.MkdirAll(filepath.Join(messageRoot, "inbox"), 0700); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(messageRoot, "inbox", "concierge")
	if err := os.WriteFile(primary, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader("Done"), &stdout, &stderr); code != 1 ||
		strings.Contains(stdout.String(), "message_id:") {
		t.Fatalf("unconfirmed publication: %d %s", code, &stdout)
	}
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader("Done"), failedOutput{}, io.Discard); code != 1 {
		t.Fatalf("send writer: %d", code)
	}
	messages, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("concierge")
	replies, err := messages.List(recipient)
	if err != nil || len(replies) != 1 {
		t.Fatalf("reply: %v %v", replies, err)
	}
	if code := Run(context.Background(), []string{"--root", root, "--message-root", messageRoot, "task", "reply", "check", replies[0].ID.String()}, strings.NewReader(""), failedOutput{}, io.Discard); code != 1 {
		t.Fatalf("check writer: %d", code)
	}
}

func TestTaskReplyErrorIncludesRecipientSelection(t *testing.T) {
	root := lifecycleRoot(t)
	var stdout bytes.Buffer
	code := Run(
		context.Background(),
		[]string{
			"--root",
			root,
			"--message-root",
			t.TempDir(),
			"task",
			"reply",
			"check",
			"missing",
		},
		strings.NewReader(""),
		&stdout,
		io.Discard,
	)
	if code != 1 || !strings.Contains(stdout.String(), "--recipient") {
		t.Fatalf("reply error must explain mailbox selection: %d %s", code, &stdout)
	}
}
