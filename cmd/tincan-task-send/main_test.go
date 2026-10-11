package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
)

type taskSessionRunner struct {
	worktree  string
	agentPane string
	agentKind string
	promptErr error
	calls     [][]string
	sessions  []string
}

func (runner *taskSessionRunner) RunSession(
	_ context.Context, session string, arguments ...string,
) ([]byte, []byte, error) {
	runner.sessions = append(runner.sessions, session)
	runner.calls = append(runner.calls, append([]string(nil), arguments...))
	switch {
	case reflect.DeepEqual(arguments, []string{"agent", "get", "pane-1"}):
		pane := runner.agentPane
		if pane == "" {
			pane = "pane-1"
		}
		kind := runner.agentKind
		if kind == "" {
			kind = "claude"
		}
		return []byte(fmt.Sprintf(
			`{"result":{"agent":{"pane_id":%q,"agent_status":"idle","agent":%q}}}`,
			pane, kind,
		)), nil, nil
	case reflect.DeepEqual(arguments, []string{"pane", "get", "pane-1"}):
		return []byte(
			fmt.Sprintf(
				`{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":%q}}}`,
				runner.worktree,
			),
		), nil, nil
	case len(arguments) == 4 && arguments[0] == "agent" && arguments[1] == "prompt" && arguments[2] == "pane-1":
		if runner.promptErr != nil {
			return nil, []byte(
				`{"error":{"code":"agent_blocked","message":"blocked"}}`,
			), runner.promptErr
		}
		return []byte(`{"result":{}}`), nil, nil
	default:
		panic(fmt.Sprintf("unexpected Herdr command: %v", arguments))
	}
}

func taskSendFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "tasks")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := strings.Join([]string{
		"window=work:pane-1", "endpoint_task_id=task-1", "worktree=" + worktree,
		"project=" + root, "harness=claude", "kind=worker", "spawn_gen=gen-1",
		"backend=herdr", "herdr_session=work", "herdr_workspace_id=workspace-1",
		"herdr_tab_id=tab-1", "herdr_pane_id=pane-1", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(crew.Record{
		TaskID: "task-1", Name: "worker-1", Kind: "claude", PaneID: "pane-1",
		HerdrSession: "work", Project: root, Worktree: worktree,
		SpawnGeneration: "gen-1", AgentArgs: []string{"--resume", "session-1"},
	}); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "messages"), worktree
}

func TestSendRecordsMessageAndNotifiesBoundWorker(t *testing.T) {
	root, messageRoot, worktree := taskSendFixture(t)
	bodyFile := filepath.Join(root, "message.txt")
	if err := os.WriteFile(bodyFile, []byte("Inspect the task.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &taskSessionRunner{worktree: worktree}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--task-root", root, "--message-root", messageRoot,
		"task-1", "--key", "task-1/first", "--body-file", bodyFile,
	}, os.Stdin, &stdout, &stderr, herdr.Client{SessionRunner: runner})
	if code != 0 || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "notification: sent\n") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	store, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("task-1")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.List(recipient)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	var assignment struct {
		Kind            string `json:"kind"`
		State           string `json:"state"`
		Task            string `json:"task"`
		SpawnGeneration string `json:"spawn_generation"`
		Body            string `json:"body"`
	}
	if err := json.Unmarshal([]byte(messages[0].Body), &assignment); err != nil {
		t.Fatalf("assignment is not JSON: %v", err)
	}
	if assignment.Kind != "task-assignment" ||
		assignment.State != filepath.Join(root, "tasks") ||
		assignment.Task != "task-1" || assignment.SpawnGeneration != "gen-1" ||
		assignment.Body != "Inspect the task.\n" {
		t.Fatalf("assignment=%+v", assignment)
	}
	for _, session := range runner.sessions {
		if session != "work" {
			t.Fatalf("Herdr session = %q, want work", session)
		}
	}
	if len(runner.calls) != 3 ||
		runner.calls[2][3] != "Pending Tincan inbox messages are available. Read and acknowledge them." {
		t.Fatalf("Herdr calls = %v", runner.calls)
	}
}

func TestSendReusesKeyForNewWorkerGeneration(t *testing.T) {
	root, messageRoot, worktree := taskSendFixture(t)
	arguments := []string{
		"--task-root", root, "--message-root", messageRoot,
		"task-1", "--key", "assignment-1", "--body-file", "-",
	}
	client := herdr.Client{SessionRunner: &taskSessionRunner{worktree: worktree}}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), arguments, strings.NewReader("Inspect the task"),
		&stdout, &stderr, client); code != 0 {
		t.Fatalf("first send code=%d stderr=%s", code, stderr.String())
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCurrentTask("task-1", func(task *crew.CurrentTask) error {
		return task.AdvanceGeneration("gen-2")
	}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), arguments, strings.NewReader("Inspect the task"),
		&stdout, &stderr, client); code != 0 {
		t.Fatalf("replacement send code=%d stderr=%s", code, stderr.String())
	}
	store, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("task-1")
	messages, err := store.List(recipient)
	if err != nil || len(messages) != 2 || messages[0].ID == messages[1].ID {
		t.Fatalf("generation messages=%+v err=%v", messages, err)
	}
	var generations []string
	for _, message := range messages {
		var assignment assignmentEnvelope
		if err := json.Unmarshal([]byte(message.Body), &assignment); err != nil {
			t.Fatal(err)
		}
		generations = append(generations, assignment.SpawnGeneration)
	}
	if !reflect.DeepEqual(generations, []string{"gen-1", "gen-2"}) {
		t.Fatalf("assignment generations=%v", generations)
	}
}

func TestSendKeepsDurableMessageWhenNotificationFails(t *testing.T) {
	root, messageRoot, worktree := taskSendFixture(t)
	runner := &taskSessionRunner{worktree: worktree, promptErr: errors.New("blocked")}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--task-root", root, "--message-root", messageRoot,
		"task-1", "--key", "task-1/blocked", "--body-file", "-",
	}, strings.NewReader("Read this later"), &stdout, &stderr, herdr.Client{SessionRunner: runner})
	if code != 0 || !strings.Contains(stdout.String(), "notification: failed\n") ||
		!strings.Contains(stderr.String(), "blocked") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	store, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("task-1")
	messages, err := store.List(recipient)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%+v err=%v", messages, err)
	}
	if body := assignmentText(t, messages[0]); body != "Read this later" {
		t.Fatalf("assignment body=%q", body)
	}
}

func TestSendRejectsTaskWithoutWorkerBinding(t *testing.T) {
	root := t.TempDir()
	messageRoot := filepath.Join(root, "messages")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--task-root", root, "--message-root", messageRoot,
		"task-1", "--body-file", "-",
	}, strings.NewReader("Do work"), &stdout, &stderr, herdr.Client{})
	if code == 0 || strings.Contains(stdout.String(), "status: recorded") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(messageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("message root exists after rejected send: %v", err)
	}
}

func TestSendRejectsBlankAssignmentWithoutMessage(t *testing.T) {
	root, messageRoot, worktree := taskSendFixture(t)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--task-root", root, "--message-root", messageRoot,
		"task-1", "--key", "blank", "--body-file", "-",
	}, strings.NewReader(" \n\t"), &stdout, &stderr,
		herdr.Client{SessionRunner: &taskSessionRunner{worktree: worktree}})
	if code == 0 || stdout.Len() != 0 {
		t.Fatalf("blank send code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(messageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blank assignment created a mailbox: %v", err)
	}
}

func TestSendWithholdsNotificationFromWrongPaneDirectory(t *testing.T) {
	root, messageRoot, _ := taskSendFixture(t)
	runner := &taskSessionRunner{worktree: t.TempDir()}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--task-root", root, "--message-root", messageRoot,
		"task-1", "--key", "task-1/wrong-pane", "--body-file", "-",
	}, strings.NewReader("Do not send to another directory"), &stdout, &stderr,
		herdr.Client{SessionRunner: runner})
	if code != 0 || !strings.Contains(stdout.String(), "notification: failed\n") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	for _, call := range runner.calls {
		if len(call) >= 2 && call[0] == "agent" && call[1] == "prompt" {
			t.Fatalf("prompted worker outside task worktree: %v", runner.calls)
		}
	}
	store, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("task-1")
	messages, err := store.List(recipient)
	if err != nil || len(messages) != 1 {
		t.Fatalf("durable message missing: messages=%+v err=%v", messages, err)
	}
}

func TestSendWithholdsNotificationFromChangedWorkerIdentity(t *testing.T) {
	for _, test := range []struct {
		name      string
		agentPane string
		agentKind string
	}{
		{name: "pane", agentPane: "pane-2"},
		{name: "kind", agentKind: "codex"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, messageRoot, worktree := taskSendFixture(t)
			runner := &taskSessionRunner{
				worktree: worktree, agentPane: test.agentPane, agentKind: test.agentKind,
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{
				"--task-root", root, "--message-root", messageRoot,
				"task-1", "--body-file", "-",
			}, strings.NewReader("Keep this"), &stdout, &stderr,
				herdr.Client{SessionRunner: runner})
			if code != 0 || !strings.Contains(stdout.String(), "notification: failed\n") {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			for _, call := range runner.calls {
				if len(call) >= 2 && call[0] == "agent" && call[1] == "prompt" {
					t.Fatalf("prompted changed worker: %v", runner.calls)
				}
			}
			store, _ := inbox.New(messageRoot)
			recipient, _ := inbox.ParseRecipient("task-1")
			messages, err := store.List(recipient)
			if err != nil || len(messages) != 1 {
				t.Fatalf("durable message missing: messages=%+v err=%v", messages, err)
			}
			if body := assignmentText(t, messages[0]); body != "Keep this" {
				t.Fatalf("assignment body=%q", body)
			}
		})
	}
}

func assignmentText(t *testing.T, message inbox.Message) string {
	t.Helper()
	var assignment assignmentEnvelope
	if err := json.Unmarshal([]byte(message.Body), &assignment); err != nil {
		t.Fatal(err)
	}
	return assignment.Body
}

func TestRetryOfAcknowledgedKeyNotifiesForOtherPendingMessage(t *testing.T) {
	root, messageRoot, worktree := taskSendFixture(t)
	runner := &taskSessionRunner{worktree: worktree}
	arguments := []string{
		"--task-root", root, "--message-root", messageRoot,
		"task-1", "--key", "task-1/first", "--body-file", "-",
	}
	var stdout, stderr bytes.Buffer
	client := herdr.Client{SessionRunner: runner}
	if code := run(context.Background(), arguments, strings.NewReader("First"), &stdout, &stderr, client); code != 0 {
		t.Fatalf("initial send: code=%d stderr=%s", code, stderr.String())
	}
	store, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("task-1")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := store.List(recipient)
	if err != nil || len(messages) != 1 {
		t.Fatalf("initial messages=%v err=%v", messages, err)
	}
	firstID := messages[0].ID
	if err := store.Acknowledge(recipient, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(recipient, "task-1/other", "Other work"); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	stdout.Reset()
	stderr.Reset()
	code := run(
		context.Background(),
		arguments,
		strings.NewReader("First"),
		&stdout,
		&stderr,
		client,
	)
	if code != 0 || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "id: "+firstID.String()+"\n") ||
		!strings.Contains(stdout.String(), "notification: sent\n") {
		t.Fatalf("retry: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if len(runner.calls) != 3 || runner.calls[2][2] != "pane-1" {
		t.Fatalf("retry Herdr calls=%v", runner.calls)
	}
	messages, err = store.List(recipient)
	if err != nil || len(messages) != 1 || messages[0].Body != "Other work" {
		t.Fatalf("pending messages=%v err=%v", messages, err)
	}
}
