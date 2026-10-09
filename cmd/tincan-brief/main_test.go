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

func TestBriefShowsEmptyState(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TINCAN_HOME", "")
	var output bytes.Buffer
	code := run(nil, &output)
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, output.String())
	}
	for _, line := range []string{"bin: ", "description: ",
		"wakes: 0 pending for firstmate", "crew: 0 registered"} {
		if !strings.Contains(output.String(), line) {
			t.Fatalf("missing %q in %q", line, output.String())
		}
	}
}

func TestBriefUsesWorkspaceStateByDefault(t *testing.T) {
	t.Chdir(t.TempDir())
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
	var output bytes.Buffer
	if code := run(nil, &output); code != 0 ||
		!strings.Contains(output.String(), "workspace wake") {
		t.Fatalf("code %d, output %q", code, output.String())
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
	code := run([]string{"--root", root}, &output)
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, output.String())
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := "bin: " + quoted(executable) + "\n" +
		"description: \"Pending primary wakes and registered local workers\"\n" +
		"wakes: 1 pending for firstmate\n" +
		"wakes[1]{id,body}:\n" +
		"  \"" + message.ID.String() + "\",\"task: task-1\\nrecord: 001.msg\\n\"\n" +
		"crew: 1 registered\n" +
		"crew[1]{task,name,session,pane_id}:\n" +
		"  \"task-1\",\"true\",\"null\",\"42\"\n" +
		"help[2]: " + quoted("tincan-inbox --root '"+root+"' read firstmate <id>") +
		"," + quoted("tincan-crew --root '"+root+"' show <task-id>") + "\n"
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
	if !strings.Contains(output.String(), "crew: 0 registered\n") ||
		!strings.Contains(output.String(), "pending_adoptions: 1 of 1\n") ||
		strings.Contains(output.String(), "crew[1]") {
		t.Fatalf("pending adoption appeared as crew: %s", output.String())
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
		{"--root", "relative-state"}, {"--root", "/tmp/state", "--limit", "0"},
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
	code := run([]string{"--root", root, "--limit", "1"}, &output)
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
