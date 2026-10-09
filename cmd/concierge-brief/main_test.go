package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/inbox"
)

const briefTaskMetadata = "window=crew:pane-1\nendpoint_task_id=task-1\nworktree=/repo/worktree\n" +
	"project=/repo\nharness=codex\nkind=ship\nspawn_gen=s123.456.7\nbackend=herdr\n" +
	"herdr_session=crew\nherdr_workspace_id=workspace-1\nherdr_tab_id=tab:1\n" +
	"herdr_pane_id=pane-1\n"

func TestBriefShowsEmptyState(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CONCIERGE_HOME", "")
	t.Setenv("TINCAN_HOME", "")
	var output bytes.Buffer
	code := run(nil, &output)
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, output.String())
	}
	for _, line := range []string{"bin: ", "description: ",
		"wakes: 0 pending for firstmate", "workers: 0 registered"} {
		if !strings.Contains(output.String(), line) {
			t.Fatalf("missing %q in %q", line, output.String())
		}
	}
}

func TestBriefUsesDistinctEnvironmentRoots(t *testing.T) {
	supervisorRoot := t.TempDir()
	messageRoot := t.TempDir()
	t.Setenv("CONCIERGE_HOME", supervisorRoot)
	t.Setenv("TINCAN_HOME", messageRoot)
	taskState := filepath.Join(supervisorRoot, "tasks")
	if err := os.Mkdir(taskState, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskState, "task-1.meta"), []byte(briefTaskMetadata), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(recipient, "env", "environment wake"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run(nil, &output); code != 0 ||
		!strings.Contains(output.String(), "tasks: 1 found") ||
		!strings.Contains(output.String(), "environment wake") ||
		!strings.Contains(output.String(), "concierge-task --root '"+supervisorRoot+"'") ||
		!strings.Contains(output.String(), "tincan-inbox --root '"+messageRoot+"'") {
		t.Fatalf("code %d, output %q", code, output.String())
	}
}

func TestBriefUsesWorkspaceStateByDefault(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CONCIERGE_HOME", "")
	t.Setenv("TINCAN_HOME", "")
	store, err := inbox.New(filepath.Join(mustWorkingDirectory(t), ".tincan"))
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(recipient, "default", "workspace wake"); err != nil {
		t.Fatal(err)
	}
	taskState := filepath.Join(mustWorkingDirectory(t), ".concierge", "tasks")
	if err := os.MkdirAll(taskState, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskState, "task-1.meta"), []byte(briefTaskMetadata), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run(nil, &output); code != 0 ||
		!strings.Contains(output.String(), "workspace wake") ||
		!strings.Contains(output.String(), "tasks: 1 found") ||
		!strings.Contains(output.String(), ".tincan'") ||
		!strings.Contains(output.String(), ".concierge'") {
		t.Fatalf("code %d, output %q", code, output.String())
	}
}

func TestBriefShowsLocalTasks(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "tasks")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(briefTaskMetadata), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root}, &output); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	for _, text := range []string{"tasks: 1 found", "tasks[1]{task,kind,worktree}:",
		"\"task-1\",\"ship\",\"/repo/worktree\""} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("missing %q in %s", text, output.String())
		}
	}
}

func TestBriefLimitsLocalTaskRows(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "tasks")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"task-1", "task-2"} {
		metadata := strings.ReplaceAll(briefTaskMetadata, "task-1", task)
		if err := os.WriteFile(filepath.Join(state, task+".meta"), []byte(metadata), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root, "--limit", "1"}, &output); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if !strings.Contains(output.String(), "tasks: 1 of 2 found") ||
		!strings.Contains(output.String(), "tasks[1]{task,kind,worktree}:") ||
		!strings.Contains(output.String(), "concierge-task --root") ||
		strings.Contains(output.String(), `"task-2","ship"`) {
		t.Fatalf("limited tasks: %s", output.String())
	}
}

func mustWorkingDirectory(t *testing.T) string {
	t.Helper()
	path, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBriefShowsPendingWakesAndRegisteredCrew(t *testing.T) {
	root := t.TempDir()
	store, err := inbox.New(root)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.Send(recipient, "task-1-stale", "task: task-1\nrecord: 001.msg\n")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(crew.Record{
		TaskID: "task-1", Name: "true", Kind: "codex", PaneID: "42",
		HerdrSession: "null", Project: "/project", Worktree: "/project-worktree",
		SpawnGeneration: "gen-1", AgentArgs: []string{"resume", "thread-1"},
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := run([]string{"--root", root, "--message-root", root}, &output)
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, output.String())
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := "bin: " + quoted(executable) + "\n" +
		"description: \"Pending primary wakes, local tasks, and registered workers\"\n" +
		"wakes: 1 pending for firstmate\n" +
		"wakes[1]{id,body}:\n" +
		"  \"" + message.ID.String() + "\",\"task: task-1\\nrecord: 001.msg\\n\"\n" +
		"tasks: 0 found\n" +
		"workers: 1 registered\n" +
		"workers[1]{task,name,session,pane_id}:\n" +
		"  \"task-1\",\"true\",\"null\",\"42\"\n" +
		"help[2]: " + quoted("tincan-inbox --root '"+root+"' read firstmate <id>") +
		"," + quoted("concierge-worker --root '"+root+"' show <task-id>") + "\n"
	if output.String() != want {
		t.Fatalf("brief = %q, want %q", output.String(), want)
	}
}

func TestBriefSeparatesPendingAdoptionFromRegisteredCrew(t *testing.T) {
	root := t.TempDir()
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(crew.Record{
		TaskID: "task-1", Name: "worker-1", Kind: "codex", PaneID: "pane-1",
		HerdrSession: "work", Project: "/project", Worktree: "/worktree",
		SpawnGeneration: "gen-1", AgentArgs: []string{"resume", "thread-1"},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "crew", "task-1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(
		data,
		[]byte(`"agent_args"`),
		[]byte(`"pending_adoption":true,"agent_args"`),
		1,
	)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root}, &output); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if !strings.Contains(output.String(), "workers: 0 registered\n") ||
		!strings.Contains(output.String(), "pending_adoptions: 1 of 1\n") ||
		strings.Contains(output.String(), "workers[1]") {
		t.Fatalf("pending adoption appeared as workers: %s", output.String())
	}
}

func TestBriefListsTruncatedPendingAdoptions(t *testing.T) {
	root := t.TempDir()
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"task-1", "task-2"} {
		if err := registry.Register(crew.Record{
			TaskID: taskID, Name: taskID, Kind: "codex", PaneID: taskID,
			HerdrSession: "work", Project: "/project", Worktree: "/worktree",
			SpawnGeneration: "gen-1", AgentArgs: []string{"resume", taskID},
		}); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "crew", taskID+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(
			data,
			[]byte(`"agent_args"`),
			[]byte(`"pending_adoption":true,"agent_args"`),
			1,
		)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root, "--limit", "1"}, &output); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if !strings.Contains(output.String(), "pending_adoptions: 1 of 2\n") ||
		strings.Count(output.String(), "show <task-id>") != 1 ||
		strings.Count(output.String(), "list") != 1 {
		t.Fatalf("missing pending list hint: %s", output.String())
	}
}

func TestBriefReportsUsageErrorsOnStdout(t *testing.T) {
	for _, args := range [][]string{
		{"--root", "/tmp/state", "--unknown"},
		{"--root", "relative-state"}, {"--message-root", "relative-state"},
		{"--root", "/tmp/state", "--limit", "0"},
	} {
		var output bytes.Buffer
		if code := run(args, &output); code != 2 ||
			!strings.Contains(output.String(), "error:") ||
			!strings.Contains(output.String(), "help:") {
			t.Fatalf("args %v: code %d, output %q", args, code, output.String())
		}
	}
}

func TestBriefLimitsStartupRowsAndReportsTotal(t *testing.T) {
	root := t.TempDir()
	store, err := inbox.New(root)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"one", "two"} {
		if _, err := store.Send(recipient, key, "wake "+key); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	code := run([]string{"--root", root, "--message-root", root, "--limit", "1"}, &output)
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "wakes: 1 of 2 pending for firstmate") ||
		!strings.Contains(output.String(), "tincan-inbox --root") ||
		strings.Count(output.String(), "wake ") != 1 {
		t.Fatalf("limited brief: %q", output.String())
	}
}

func TestBriefVersionFlags(t *testing.T) {
	for _, flag := range []string{"-v", "-V", "--version"} {
		var output bytes.Buffer
		if code := run([]string{flag}, &output); code != 0 || output.String() != "0.1.0\n" {
			t.Fatalf("flag %s: code %d, output %q", flag, code, output.String())
		}
	}
}
