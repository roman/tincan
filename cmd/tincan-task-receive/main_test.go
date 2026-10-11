package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/inbox"
)

func receiveFixture(t *testing.T) (string, string, inbox.Message) {
	t.Helper()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := strings.Join([]string{
		"window=work:pane-1", "endpoint_task_id=task-1", "worktree=" + worktree,
		"project=" + root, "harness=codex", "kind=crewmate", "spawn_gen=gen-1",
		"backend=herdr", "herdr_session=work", "herdr_workspace_id=workspace-1",
		"herdr_tab_id=tab-1", "herdr_pane_id=pane-1", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "tasks", "task-1.meta"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(crew.Record{
		TaskID: "task-1", Name: "worker-1", Kind: "codex", PaneID: "pane-1",
		HerdrSession: "work", Project: root, Worktree: worktree,
		SpawnGeneration: "gen-1", AgentArgs: []string{"resume", "thread-1"},
	}); err != nil {
		t.Fatal(err)
	}
	messageRoot := filepath.Join(root, "messages")
	store, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	body, _ := json.Marshal(map[string]string{
		"kind": "task-assignment", "state": filepath.Join(root, "tasks"),
		"task": "task-1", "spawn_generation": "gen-1", "body": "Build feature",
	})
	message, err := store.Send(recipient, "assignment", string(body))
	if err != nil {
		t.Fatal(err)
	}
	return root, messageRoot, message
}

func TestReadAssignmentChecksWorkerGeneration(t *testing.T) {
	root, messageRoot, message := receiveFixture(t)
	for _, test := range []struct {
		name       string
		generation string
		status     string
		body       bool
	}{
		{name: "current", generation: "gen-1", status: "current", body: true},
		{name: "old worker", generation: "gen-old", status: "stale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			code := run([]string{
				"--task-root", root, "--message-root", messageRoot,
				"read", "task-1", message.ID.String(), "--generation", test.generation,
			}, &output)
			if code != 0 || !strings.Contains(output.String(), "status: "+test.status+"\n") ||
				strings.Contains(output.String(), "body:") != test.body {
				t.Fatalf("code=%d output=%q", code, output.String())
			}
		})
	}
}

func TestAcknowledgeAssignmentChecksGeneration(t *testing.T) {
	root, messageRoot, message := receiveFixture(t)
	for _, generation := range []string{"gen-old", "gen-1", "gen-1"} {
		var output bytes.Buffer
		code := run([]string{
			"--task-root", root, "--message-root", messageRoot,
			"ack", "task-1", message.ID.String(), "--generation", generation,
		}, &output)
		if code != 0 {
			t.Fatalf("generation %q: code=%d output=%q", generation, code, output.String())
		}
		store, _ := inbox.New(messageRoot)
		recipient, _ := inbox.ParseRecipient("task-1")
		pending, err := store.List(recipient)
		if err != nil {
			t.Fatal(err)
		}
		wantPending := generation == "gen-old"
		if (len(pending) != 0) != wantPending {
			t.Fatalf("generation %q: pending=%d output=%q", generation, len(pending), output.String())
		}
	}
}

func TestCurrentWorkerDisposesOlderAssignment(t *testing.T) {
	root, messageRoot, message := receiveFixture(t)
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCurrentTask("task-1", func(task *crew.CurrentTask) error {
		return task.AdvanceGeneration("gen-2")
	}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		generation string
		status     string
		pending    bool
	}{
		{generation: "gen-1", status: "stale", pending: true},
		{generation: "gen-2", status: "stale", pending: false},
		{generation: "gen-1", status: "stale", pending: false},
		{generation: "gen-2", status: "acknowledged", pending: false},
	} {
		var output bytes.Buffer
		code := run([]string{
			"--task-root", root, "--message-root", messageRoot,
			"ack", "task-1", message.ID.String(), "--generation", test.generation,
		}, &output)
		if code != 0 || !strings.Contains(output.String(), "status: "+test.status+"\n") {
			t.Fatalf("generation=%s code=%d output=%q", test.generation, code, output.String())
		}
		store, _ := inbox.New(messageRoot)
		recipient, _ := inbox.ParseRecipient("task-1")
		pending, err := store.List(recipient)
		if err != nil || (len(pending) != 0) != test.pending {
			t.Fatalf("generation=%s pending=%d err=%v", test.generation, len(pending), err)
		}
	}
}

func TestAcknowledgeRejectsUnrelatedMessage(t *testing.T) {
	root, messageRoot, _ := receiveFixture(t)
	store, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("task-1")
	message, err := store.Send(recipient, "unrelated", `{"kind":"task-bootstrap"}`)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := run([]string{
		"--task-root", root, "--message-root", messageRoot,
		"ack", "task-1", message.ID.String(), "--generation", "gen-1",
	}, &output)
	if code != 1 || !strings.Contains(output.String(), "not an assignment") {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
	if _, err := store.Read(recipient, message.ID); err != nil {
		t.Fatalf("unrelated message was acknowledged: %v", err)
	}
}
