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
	"github.com/roman/tincan/pkg/notify"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func assignmentSendFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := shortTaskRoot(t)
	worktree := t.TempDir()
	worker := taskstate.Worker{
		Name:       "worker",
		Harness:    "codex",
		Session:    "work",
		Pane:       "pane-1",
		Workspace:  "workspace-1",
		Tab:        "tab-1",
		Project:    worktree,
		Worktree:   worktree,
		Generation: "gen-1",
		AgentArgs:  []string{"resume", "thread-1"},
	}
	seedViewTask(t, root, "task-1", taskstate.InFlight, &worker, nil)
	directory := t.TempDir()
	record := filepath.Join(t.TempDir(), "prompts")
	script := `#!/bin/sh
[ "$HERDR_SESSION" = work ] || exit 71
[ "$TEST_NOTIFY_FAILURE" = 1 ] && exit 72
case "$1 $2" in
 'pane get') printf '{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":"%s"}}}\n' "$TEST_WORKTREE" ;;
 'agent get') printf '%s\n' '{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":"codex"}}}' ;;
 'agent prompt') printf '%s\n' "$3" "$4" >> "$PROMPT_RECORD"; printf '%s\n' '{"result":{}}' ;;
 *) exit 73 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("HERDR_SESSION", "wrong-ambient-session")
	t.Setenv("TEST_WORKTREE", worktree)
	t.Setenv("PROMPT_RECORD", record)
	t.Setenv("TEST_NOTIFY_FAILURE", "")
	return root, t.TempDir(), record
}

func TestTaskSendPreservesDeliveryOnNotificationFailureAndRetry(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	t.Setenv(workerLaunchGeneration, "gen-1")
	root, messageRoot, record := assignmentSendFixture(t)
	serveHerdrFixture(t, root, messageRoot)
	body := "Complete assignment\n\"quoted\" ✓\n"
	path := filepath.Join(t.TempDir(), "body")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--message-root",
		messageRoot,
		"task",
		"send",
		"task-1",
		"--key",
		"brief",
		"--body-file",
		path,
	}
	t.Setenv("TEST_NOTIFY_FAILURE", "1")
	failed := expectLifecycleError(t, root, 1, args...)
	messages, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("task-1")
	pending, err := messages.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("delivery lost: %v %v", pending, err)
	}
	id := pending[0].ID.String()
	prefix := "message_id: \"" + id + "\"\nnotification: "
	if !strings.HasPrefix(failed, prefix+"failed\n") || !strings.Contains(failed, "retry") {
		t.Fatalf("lost delivery result: %s", failed)
	}
	envelope, err := taskprotocol.DecodeAssignment(pending[0].Body, root, "task-1")
	if err != nil || envelope.Body != body || envelope.SpawnGeneration != "gen-1" {
		t.Fatalf("assignment: %#v %v", envelope, err)
	}
	t.Setenv("TEST_NOTIFY_FAILURE", "")
	args[len(args)-1] = "-"
	if got := invoke(t, root, body, args...); got != prefix+"sent\n" {
		t.Fatalf("retry: %s", got)
	}
	pending, err = messages.List(recipient)
	if err != nil || len(pending) != 1 || pending[0].ID.String() != id {
		t.Fatalf("duplicate retry: %v %v", pending, err)
	}
	prompt, err := os.ReadFile(record)
	if err != nil || string(prompt) != "pane-1\n"+notify.DoorbellText+"\n" {
		t.Fatalf("prompt: %q %v", prompt, err)
	}
	invoke(
		t,
		root,
		"",
		"--message-root",
		messageRoot,
		"task",
		"ack",
		"task-1",
		id,
		"--generation",
		"gen-1",
	)
	if got := invoke(t, root, body, args...); got != prefix+"acknowledged\n" {
		t.Fatalf("handled retry: %s", got)
	}
	after, err := os.ReadFile(record)
	if err != nil || !bytes.Equal(prompt, after) {
		t.Fatalf("prompted handled assignment: %q %v", after, err)
	}
}

func TestTaskSendFailuresDoNotInventDelivery(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	root, messageRoot, _ := assignmentSendFixture(t)
	serveHerdrFixture(t, root, messageRoot)
	args := []string{"--message-root", messageRoot, "task", "send", "task-1", "--body-file", "-"}
	expectLifecycleError(t, root, 2, args[:len(args)-2]...)
	t.Setenv("TEST_NOTIFY_FAILURE", "1")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader("Work"), &stdout, &stderr); code != 1 ||
		!strings.Contains(stdout.String(), "message_id:") ||
		strings.Contains(stdout.String(), "retry") {
		t.Fatalf("unkeyed guidance: %d %s %s", code, &stdout, &stderr)
	}
	failedRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(failedRoot, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	args[1] = failedRoot
	stdout.Reset()
	if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader("Work"), &stdout, &stderr); code != 2 ||
		strings.Contains(stdout.String(), "message_id:") {
		t.Fatalf("unconfirmed delivery: %d %s", code, &stdout)
	}
	args[1] = messageRoot
	if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader("Work"), failedOutput{}, io.Discard); code != 1 {
		t.Fatalf("writer failure: %d", code)
	}
}
