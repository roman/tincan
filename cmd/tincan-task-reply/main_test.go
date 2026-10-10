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

func replyFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	tasks := filepath.Join(root, "tasks")
	if err := os.Mkdir(tasks, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := strings.Join([]string{
		"window=work:pane-1", "endpoint_task_id=task-1", "worktree=" + worktree,
		"project=" + root, "harness=claude", "kind=worker", "spawn_gen=gen-1",
		"backend=herdr", "herdr_session=work", "herdr_workspace_id=workspace-1",
		"herdr_tab_id=tab-1", "herdr_pane_id=pane-1", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tasks, "task-1.meta"), []byte(metadata), 0o600); err != nil {
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
	return root, filepath.Join(root, "messages")
}

func TestReplySendAndCheckCurrentGeneration(t *testing.T) {
	root, messages := replyFixture(t)
	args := []string{"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"}
	var output, errors bytes.Buffer
	if code := run(args, strings.NewReader("Done.\n"), &output, &errors); code != 0 {
		t.Fatalf("send code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	var envelope map[string]string
	if err := json.Unmarshal([]byte(pending[0].Body), &envelope); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"kind": "task-reply", "state": filepath.Join(root, "tasks"),
		"task": "task-1", "spawn_generation": "gen-1", "body": "Done.\n",
	} {
		if envelope[key] != want {
			t.Fatalf("envelope[%s]=%q, want %q", key, envelope[key], want)
		}
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", pending[0].ID.String()}, strings.NewReader(""), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "status: current\n") ||
		!strings.Contains(output.String(), `body: "Done.\n"`) {
		t.Fatalf("check code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
	output.Reset()
	errors.Reset()
	if code := run(args, strings.NewReader("Done.\n"), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "id: "+pending[0].ID.String()) {
		t.Fatalf("retry code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
}

func TestReplyRejectsOldGenerationAtSendAndCheck(t *testing.T) {
	root, messages := replyFixture(t)
	var output, errors bytes.Buffer
	args := []string{"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"}
	if code := run(args, strings.NewReader("Finished"), &output, &errors); code != 0 {
		t.Fatalf("send code=%d errors=%s", code, errors.String())
	}
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	metaPath := filepath.Join(root, "tasks", "task-1.meta")
	metadata, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath,
		[]byte(strings.Replace(string(metadata), "spawn_gen=gen-1", "spawn_gen=gen-2", 1)),
		0o600); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "crew", "task-1.json")
	recordData, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record crew.Record
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	record.SpawnGeneration = "gen-2"
	recordData, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, recordData, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", pending[0].ID.String()}, strings.NewReader(""), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "status: stale\n") ||
		strings.Contains(output.String(), "body:") {
		t.Fatalf("stale check code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
	output.Reset()
	errors.Reset()
	if code := run(args, strings.NewReader("Finished"), &output, &errors); code == 0 {
		t.Fatalf("stale send succeeded: %s", output.String())
	}
	pending, err = store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("stale send changed inbox: %+v err=%v", pending, err)
	}
}

func TestReplyCheckRejectsOrdinaryMessage(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	message, err := store.Send(recipient, "ordinary", "Unbound text")
	if err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", message.ID.String()}, strings.NewReader(""), &output, &errors); code == 0 ||
		strings.Contains(output.String(), "status: current") {
		t.Fatalf("ordinary message accepted: code=%d output=%s", code, output.String())
	}
}

func TestReplyUsesSameTaskStateThroughSymlink(t *testing.T) {
	root, messages := replyFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	if code := run([]string{"--task-root", alias, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"},
		strings.NewReader("Done"), &output, &errors); code != 0 {
		t.Fatalf("send code=%d errors=%s", code, errors.String())
	}
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", pending[0].ID.String()}, strings.NewReader(""), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "status: current\n") {
		t.Fatalf("check code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"},
		strings.NewReader("Done"), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "id: "+pending[0].ID.String()) {
		t.Fatalf("retry code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
}

func TestReplyCheckTreatsRemovedTaskAsStale(t *testing.T) {
	root, messages := replyFixture(t)
	var output, errors bytes.Buffer
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"},
		strings.NewReader("Done"), &output, &errors); code != 0 {
		t.Fatalf("send code=%d errors=%s", code, errors.String())
	}
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := os.Remove(filepath.Join(root, "tasks", "task-1.meta")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", pending[0].ID.String()}, strings.NewReader(""), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "status: stale\n") ||
		strings.Contains(output.String(), "body:") {
		t.Fatalf("check code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
}
