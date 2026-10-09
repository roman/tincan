package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/herdr"
)

type liveRunner struct {
	worktree  string
	status    string
	process   string
	commands  []string
	onCommand func([]string)
}

func (runner *liveRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.commands = append(runner.commands, session+":"+strings.Join(args, " "))
	if runner.onCommand != nil {
		runner.onCommand(args)
	}
	if session != "work" {
		return nil, nil, fmt.Errorf("wrong session %s", session)
	}
	switch strings.Join(args[:2], " ") {
	case "pane get":
		return []byte(
			fmt.Sprintf(
				`{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":%q,"workspace_id":"ws-1","tab_id":"tab-1"}}}`,
				runner.worktree,
			),
		), nil, nil
	case "agent get":
		return []byte(
			fmt.Sprintf(
				`{"result":{"agent":{"pane_id":"pane-1","agent_status":%q,"agent":"codex"}}}`,
				runner.status,
			),
		), nil, nil
	case "pane process-info":
		return []byte(
			fmt.Sprintf(
				`{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":%q,"argv0":%q}]}}}`,
				runner.process,
				runner.process,
			),
		), nil, nil
	}
	return nil, nil, fmt.Errorf("unexpected command %v", args)
}

func adoptionArguments(root, worktree string) []string {
	return []string{
		"--root",
		root,
		"adopt",
		"task-1",
		"--session",
		"work",
		"--pane",
		"pane-1",
		"--name",
		"worker-1",
		"--project",
		root,
		"--worktree",
		worktree,
		"--agent-arg",
		"resume",
		"--agent-arg",
		"thread-1",
	}
}

func TestAdoptPublishesTaskAndCrewAndRetries(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &liveRunner{worktree: worktree, status: "working", process: "codex"}
	client := herdr.Client{SessionRunner: runner}
	arguments := adoptionArguments(root, worktree)
	for attempt := 0; attempt < 2; attempt++ {
		var output bytes.Buffer
		if code := run(context.Background(), arguments, &output, client); code != 0 {
			t.Fatalf("attempt %d: code=%d output=%s", attempt, code, output.String())
		}
		if attempt == 0 {
			if err := os.Remove(filepath.Join(root, "crew", "task-1.json")); err != nil {
				t.Fatal(err)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "tasks", "task-1.meta"))
	if err != nil || !strings.Contains(string(data), "herdr_workspace_id=ws-1\n") {
		t.Fatalf("metadata=%q err=%v", data, err)
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := registry.Get("task-1")
	if err != nil || record.Name != "worker-1" || record.SpawnGeneration == "" {
		t.Fatalf("crew=%+v err=%v", record, err)
	}
	if !strings.Contains(string(data), "spawn_gen="+record.SpawnGeneration+"\n") {
		t.Fatalf("generation mismatch: %q", data)
	}
	path := filepath.Join(root, "crew", "task-1.json")
	crewData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	crewData = bytes.Replace(
		crewData,
		[]byte(`"agent_args"`),
		[]byte(`"pending_adoption":true,"agent_args"`),
		1,
	)
	if err := os.WriteFile(path, crewData, 0o600); err != nil {
		t.Fatal(err)
	}
	runner.status = "done"
	runner.process = "zsh"
	beforeRecovery := len(runner.commands)
	var recoveryOutput bytes.Buffer
	if code := run(context.Background(), arguments, &recoveryOutput, client); code != 0 {
		t.Fatalf("stopped-worker recovery: code=%d output=%s", code, recoveryOutput.String())
	}
	if len(runner.commands) != beforeRecovery {
		t.Fatalf("recovery queried a stopped worker: %v", runner.commands[beforeRecovery:])
	}
	recovered, err := registry.Get("task-1")
	if err != nil || recovered.PendingAdoption {
		t.Fatalf("recovered record=%+v err=%v", recovered, err)
	}
	for _, command := range runner.commands {
		if !strings.HasPrefix(command, "work:") {
			t.Fatalf("unbound command %q", command)
		}
	}
}

func TestAdoptRefusesUnverifiedWorkerWithoutPublishing(t *testing.T) {
	for _, test := range []struct{ name, directory, status, process string }{
		{name: "wrong directory", directory: "/other", status: "working", process: "codex"},
		{name: "shell only", status: "working", process: "zsh"},
		{name: "done agent", status: "done", process: "codex"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			worktree := filepath.Join(root, "worktree")
			if err := os.Mkdir(worktree, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "tasks"), 0o700); err != nil {
				t.Fatal(err)
			}
			directory := test.directory
			if directory == "" {
				directory = worktree
			}
			runner := &liveRunner{worktree: directory, status: test.status, process: test.process}
			var output bytes.Buffer
			if code := run(context.Background(), adoptionArguments(root, worktree), &output, herdr.Client{SessionRunner: runner}); code == 0 {
				t.Fatalf("unexpected success: %s", output.String())
			}
			if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.meta")); !os.IsNotExist(
				err,
			) {
				t.Fatalf("metadata: %v", err)
			}
		})
	}
}

func TestAdoptRejectsCrewConflictBeforeTaskPublication(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(crew.Record{
		TaskID: "another-task", Name: "worker-1", Kind: "codex", PaneID: "another-pane",
		HerdrSession: "work", Project: root, Worktree: worktree,
		SpawnGeneration: "gen-other", AgentArgs: []string{"resume", "other"},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &liveRunner{worktree: worktree, status: "working", process: "codex"}
	var output bytes.Buffer
	if code := run(context.Background(), adoptionArguments(root, worktree), &output, herdr.Client{SessionRunner: runner}); code == 0 {
		t.Fatalf("unexpected success: %s", output.String())
	}
	if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.meta")); !os.IsNotExist(err) {
		t.Fatalf("conflict left metadata: %v", err)
	}
}

func TestAdoptRejectsReplacedTaskDirectory(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &liveRunner{worktree: worktree, status: "working", process: "codex"}
	replaced := false
	runner.onCommand = func(args []string) {
		if replaced || len(args) < 2 || args[0] != "pane" || args[1] != "get" {
			return
		}
		replaced = true
		if err := os.Rename(filepath.Join(root, "tasks"), filepath.Join(root, "tasks-old")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "tasks"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if code := run(context.Background(), adoptionArguments(root, worktree), &output, herdr.Client{SessionRunner: runner}); code == 0 {
		t.Fatalf("unexpected success: %s", output.String())
	}
	for _, directory := range []string{"tasks", "tasks-old"} {
		if _, err := os.Lstat(filepath.Join(root, directory, "task-1.meta")); !os.IsNotExist(err) {
			t.Fatalf("%s contains metadata: %v", directory, err)
		}
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := registry.Get("task-1")
	if err != nil || !reservation.PendingAdoption {
		t.Fatalf("pending reservation=%+v err=%v", reservation, err)
	}
	if err := registry.Register(crew.Record{
		TaskID: "another-task", Name: "worker-1", Kind: "codex", PaneID: "another-pane",
		HerdrSession: "work", Project: root, Worktree: worktree,
		SpawnGeneration: "gen-other", AgentArgs: []string{"resume", "other"},
	}); err != crew.ErrConflict {
		t.Fatalf("rival registration: %v", err)
	}
	runner.status = "done"
	output.Reset()
	if code := run(context.Background(), []string{"--root", root, "cancel", "task-1"}, &output, herdr.Client{SessionRunner: runner}); code != 0 {
		t.Fatalf("cancel code=%d output=%s", code, output.String())
	}
	if _, err := registry.Get("task-1"); !os.IsNotExist(err) {
		t.Fatalf("reservation remained: %v", err)
	}
}

func TestCancelKeepsActiveTaskAndPublishedPendingTask(t *testing.T) {
	for _, test := range []struct {
		name     string
		pending  bool
		metadata bool
	}{
		{name: "active crew without metadata"},
		{name: "pending adoption with metadata", pending: true, metadata: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "tasks"), 0o700); err != nil {
				t.Fatal(err)
			}
			registry, err := crew.New(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(crew.Record{
				TaskID: "task-1", Name: "worker-1", Kind: "codex", PaneID: "pane-1",
				HerdrSession: "work", Project: root, Worktree: root,
				SpawnGeneration: "gen-1", AgentArgs: []string{"resume", "thread-1"},
			}); err != nil {
				t.Fatal(err)
			}
			if test.pending {
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
			}
			if test.metadata {
				data := fmt.Sprintf(
					"window=work:pane-1\nendpoint_task_id=task-1\nworktree=%s\nproject=%s\nharness=codex\nkind=crewmate\nspawn_gen=gen-1\nbackend=herdr\nherdr_session=work\nherdr_workspace_id=ws-1\nherdr_tab_id=tab-1\nherdr_pane_id=pane-1\n",
					root,
					root,
				)
				if err := os.WriteFile(filepath.Join(root, "tasks", "task-1.meta"), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			if code := run(context.Background(), []string{"--root", root, "cancel", "task-1"}, &output, herdr.Client{}); code == 0 {
				t.Fatalf("unexpected cancellation: %s", output.String())
			}
			record, err := registry.Get("task-1")
			if err != nil || record.PendingAdoption != test.pending {
				t.Fatalf("record changed: %+v err=%v", record, err)
			}
			if test.metadata {
				if _, err := os.Stat(filepath.Join(root, "tasks", "task-1.meta")); err != nil {
					t.Fatalf("metadata removed: %v", err)
				}
			}
		})
	}
}
