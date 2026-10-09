package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
)

func TestShowReportsPendingGeneration(t *testing.T) {
	root := t.TempDir()
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	record := crew.Record{
		TaskID: "task-1", Name: "worker-1", Kind: "codex", PaneID: "pane-1",
		HerdrSession: "work", Project: "/project", Worktree: "/project-worktree",
		SpawnGeneration: "gen-1", AgentArgs: []string{"resume", "thread-1"},
	}
	if err := registry.Register(record); err != nil {
		t.Fatal(err)
	}
	record.PendingGeneration = "gen-2"
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "crew", "task-1.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root, "show", "task-1"}, &output); code != 0 {
		t.Fatalf("show code %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "pending_generation: gen-2\n") {
		t.Fatalf("pending transition missing from %q", output.String())
	}
}

func writeTaskMetadata(t *testing.T, state string) {
	t.Helper()
	data := strings.Join([]string{
		"window=work:pane-1", "endpoint_task_id=task-1", "worktree=/project-worktree",
		"project=/project", "harness=codex", "kind=crewmate", "spawn_gen=gen-1",
		"backend=herdr", "herdr_session=work", "herdr_workspace_id=workspace-1",
		"herdr_tab_id=tab-1", "herdr_pane_id=pane-1", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterAndShowExistingLocalTask(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "tasks")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTaskMetadata(t, state)
	var stdout bytes.Buffer
	code := run([]string{"--root", root, "register", "task-1",
		"--name", "worker-1", "--agent-arg", "resume", "--agent-arg", "thread-1"},
		&stdout)
	if code != 0 {
		t.Fatalf("register code %d: %s", code, stdout.String())
	}
	stdout.Reset()
	code = run([]string{"--root", root, "show", "task-1"}, &stdout)
	if code != 0 {
		t.Fatalf("show code %d: %s", code, stdout.String())
	}
	for _, fragment := range []string{"task: task-1", "name: worker-1", "kind: codex",
		"pane_id: pane-1", "herdr_session: work", "spawn_generation: gen-1", "agent_args[2]:"} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Fatalf("output %q missing %q", stdout.String(), fragment)
		}
	}
}

func TestRegisterRequiresResumeArguments(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "tasks")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTaskMetadata(t, state)
	var stdout bytes.Buffer
	code := run([]string{"--root", root, "register", "task-1",
		"--name", "worker-1"}, &stdout)
	if code != 2 || !strings.Contains(stdout.String(), "--agent-arg") {
		t.Fatalf("code %d, output %q", code, stdout.String())
	}
}

func TestListEmptyCrewIsDefinitive(t *testing.T) {
	var stdout bytes.Buffer
	code := run([]string{"--root", t.TempDir(), "list"}, &stdout)
	if code != 0 || stdout.String() != "count: 0\ncrew[0]{task,name,session,pane_id}:\n" {
		t.Fatalf("code %d, output %q", code, stdout.String())
	}
}

func TestListShowsRegisteredCrewInTaskOrder(t *testing.T) {
	root := t.TempDir()
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []crew.Record{
		{TaskID: "task-2", Name: "worker-2", Kind: "codex", PaneID: "pane-2",
			HerdrSession: "work", Project: "/project", Worktree: "/worktree-2",
			SpawnGeneration: "gen-2", AgentArgs: []string{"resume", "thread-2"}},
		{TaskID: "task-1", Name: "worker-1", Kind: "claude", PaneID: "pane-1",
			HerdrSession: "work", Project: "/project", Worktree: "/worktree-1",
			SpawnGeneration: "gen-1", AgentArgs: []string{"resume", "thread-1"}},
	} {
		if err := registry.Register(record); err != nil {
			t.Fatal(err)
		}
	}
	var stdout bytes.Buffer
	if code := run([]string{"--root", root, "list"}, &stdout); code != 0 {
		t.Fatalf("code %d, output %q", code, stdout.String())
	}
	want := "count: 2\ncrew[2]{task,name,session,pane_id}:\n" +
		"  task-1,worker-1,work,pane-1\n  task-2,worker-2,work,pane-2\n"
	if stdout.String() != want {
		t.Fatalf("got %q, want %q", stdout.String(), want)
	}
}

func TestUnknownRegisterFlagGivesRegisterHelp(t *testing.T) {
	var stdout bytes.Buffer
	code := run([]string{"--root", t.TempDir(), "register", "task-1", "--stat", "open"}, &stdout)
	if code != 2 ||
		!strings.Contains(stdout.String(), "register flags: --root, --name, --agent-arg, --help") {
		t.Fatalf("code %d, output %q", code, stdout.String())
	}
}

func TestInvalidShowTaskIDIsUsageError(t *testing.T) {
	var stdout bytes.Buffer
	code := run([]string{"--root", t.TempDir(), "show", "../escape"}, &stdout)
	if code != 2 || !strings.Contains(stdout.String(), "show flags:") {
		t.Fatalf("code %d, output %q", code, stdout.String())
	}
}

func TestInvalidWorkerNameIsUsageError(t *testing.T) {
	var stdout bytes.Buffer
	code := run(
		[]string{
			"--root",
			t.TempDir(),
			"register",
			"task-1",
			"--name",
			"bad name",
			"--agent-arg",
			"resume",
		},
		&stdout,
	)
	if code != 2 || !strings.Contains(stdout.String(), "valid --name") {
		t.Fatalf("code %d, output %q", code, stdout.String())
	}
}

func TestMissingTaskStateDoesNotSuggestLockRetry(t *testing.T) {
	var stdout bytes.Buffer
	code := run(
		[]string{
			"--root",
			t.TempDir(),
			"register",
			"task-1",
			"--name",
			"worker-1",
			"--agent-arg",
			"resume",
		},
		&stdout,
	)
	if code != 1 || !strings.Contains(stdout.String(), "create the local task state directory") ||
		strings.Contains(stdout.String(), "retry after the task metadata lock") {
		t.Fatalf("code %d, output %q", code, stdout.String())
	}
}

func TestRegistrationWaitsForTaskMetadataLock(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "tasks")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTaskMetadata(t, state)
	statePath, _ := firstmate.ParseAbsolutePath(state)
	id, _ := firstmate.ParseTaskID("task-1")
	lock, err := firstmate.AcquireTaskLock(statePath, id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	var stdout bytes.Buffer
	code := run(
		[]string{
			"--root",
			root,
			"register",
			"task-1",
			"--name",
			"worker-1",
			"--agent-arg",
			"resume",
		},
		&stdout,
	)
	if code != 1 || !strings.Contains(stdout.String(), "lock") {
		t.Fatalf("code %d, output %q", code, stdout.String())
	}
	registry, _ := crew.New(root)
	records, err := registry.List()
	if err != nil || len(records) != 0 {
		t.Fatalf("records %+v, error %v", records, err)
	}
}
