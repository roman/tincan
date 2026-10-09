package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/crew"
)

func backlogRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".tincan")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func queueTask(t *testing.T, root, id string, extra ...string) {
	t.Helper()
	arguments := []string{
		"--root",
		root,
		"add",
		id,
		"Dispatch task",
		"--kind",
		"ship",
		"--repo",
		"exmachina",
	}
	arguments = append(arguments, extra...)
	var output bytes.Buffer
	if code := run(arguments, &output); code != 0 {
		t.Fatalf("queue task: %s", output.String())
	}
}

func bindWorker(t *testing.T, root, id string) {
	t.Helper()
	state := filepath.Join(root, "tasks")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	data := strings.Join([]string{
		"window=work:pane-1", "endpoint_task_id=" + id, "worktree=/project-worktree",
		"project=/project", "harness=codex", "kind=crewmate", "spawn_gen=gen-1",
		"backend=herdr", "herdr_session=work", "herdr_workspace_id=workspace-1",
		"herdr_tab_id=tab-1", "herdr_pane_id=pane-1", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(state, id+".meta"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(crew.Record{
		TaskID: id, Name: "worker-1", Kind: "codex", PaneID: "pane-1", HerdrSession: "work",
		Project: "/project", Worktree: "/project-worktree", SpawnGeneration: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}); err != nil {
		t.Fatal(err)
	}
}

func backlogState(t *testing.T, root, id string) string {
	t.Helper()
	output, err := exec.Command("tasks-axi", "show", id, "--file", filepath.Join(root, "backlog.md")).
		CombinedOutput()
	if err != nil {
		t.Fatalf("show task: %v: %s", err, output)
	}
	return string(output)
}

func TestStartRequiresCurrentWorkerBinding(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	var output bytes.Buffer
	if code := run([]string{"--root", root, "start", "task-1"}, &output); code == 0 {
		t.Fatalf("start without worker succeeded: %s", output.String())
	}
	if !strings.Contains(output.String(), "current") {
		t.Fatalf("missing worker diagnosis: %s", output.String())
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: queued") {
		t.Fatal("backlog row changed without a worker")
	}
}

func TestStartRejectsUndispatchableTask(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{name: "blocked", want: "task is blocked", prepare: func(t *testing.T, root string) {
			queueTask(t, root, "dependency-1")
			queueTask(t, root, "task-1", "--blocked-by", "dependency-1")
		}},
		{name: "held", want: "task is held", prepare: func(t *testing.T, root string) {
			queueTask(t, root, "task-1")
			output, err := exec.Command("tasks-axi", "hold", "task-1", "--reason", "Waiting for review", "--file", filepath.Join(root, "backlog.md")).CombinedOutput()
			if err != nil {
				t.Fatalf("hold task: %v: %s", err, output)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := backlogRoot(t)
			test.prepare(t, root)
			bindWorker(t, root, "task-1")
			var output bytes.Buffer
			if code := run([]string{"--root", root, "start", "task-1"}, &output); code == 0 {
				t.Fatalf("undispatchable start succeeded: %s", output.String())
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("missing diagnosis: %s", output.String())
			}
			if !strings.Contains(backlogState(t, root, "task-1"), "state: queued") {
				t.Fatal("undispatchable backlog row changed")
			}
		})
	}
}

func TestStartTransitionsAndRetriesAfterInterruption(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	backlogFile := filepath.Join(root, "backlog.md")
	if err := os.Chmod(backlogFile, 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		var output bytes.Buffer
		if code := run([]string{"--root", root, "start", "task-1"}, &output); code != 0 {
			t.Fatalf("start attempt %d: %s", attempt, output.String())
		}
		wantAlready := "already: false"
		if attempt == 1 {
			wantAlready = "already: true"
		}
		if !strings.Contains(output.String(), wantAlready) {
			t.Fatalf("start attempt %d: %s", attempt, output.String())
		}
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: in_flight") {
		t.Fatal("backlog row did not enter in flight")
	}
	info, err := os.Stat(backlogFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("backlog mode changed to %o", info.Mode().Perm())
	}
}

func TestStartSharesTasksAxiBacklogLock(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	backlogFile := filepath.Join(root, "backlog.md")
	lockFile := backlogFile + ".lock"
	if err := os.WriteFile(lockFile, []byte("test owner\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root, "start", "task-1"}, &output); code == 0 {
		t.Fatalf("start ignored backlog lock: %s", output.String())
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: queued") {
		t.Fatal("backlog row changed while locked")
	}
	if err := os.Remove(lockFile); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--root", root, "start", "task-1"}, &output); code != 0 {
		t.Fatalf("start after lock release failed: %s", output.String())
	}
}

func TestTasksAxiRespectsTincanBacklogLock(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	backlogFile := filepath.Join(root, "backlog.md")
	unlock, err := lockBacklog(backlogFile)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("tasks-axi", "hold", "task-1", "--reason", "Waiting for review", "--file", backlogFile).
		CombinedOutput()
	if err == nil || !strings.Contains(string(output), "backlog is locked") {
		t.Fatalf("tasks-axi ignored Tincan lock: %v: %s", err, output)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "held: no") {
		t.Fatal("tasks-axi changed the backlog while Tincan held its lock")
	}
}

func TestStartRecoversDeadTincanLock(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	lockFile := filepath.Join(root, "backlog.md.lock")
	if err := os.WriteFile(lockFile, []byte("99999999:tincan-start\n"), 0600); err != nil {
		t.Fatal(err)
	}
	staleTime := time.Now().Add(-time.Minute)
	if err := os.Chtimes(lockFile, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root, "start", "task-1"}, &output); code != 0 {
		t.Fatalf("start after dead owner failed: %s", output.String())
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: in_flight") {
		t.Fatal("stale lock recovery did not dispatch")
	}
	if _, err := os.Lstat(lockFile); !os.IsNotExist(err) {
		t.Fatalf("lock remained after recovery: %v", err)
	}
}

func TestStartPreservesLocksWithoutDeadTincanOwner(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		age     time.Duration
	}{
		{name: "live owner", content: fmt.Sprintf("%d:tincan-start\n", os.Getpid()), age: time.Minute},
		{name: "young dead owner", content: "99999999:tincan-start\n", age: time.Second},
		{name: "tasks-axi owner", content: "99999999:nonce:123:1\n", age: time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := backlogRoot(t)
			lockFile := filepath.Join(root, "backlog.md.lock")
			if err := os.WriteFile(lockFile, []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			stamp := time.Now().Add(-test.age)
			if err := os.Chtimes(lockFile, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if unlock, err := lockBacklog(filepath.Join(root, "backlog.md")); err == nil {
				unlock()
				t.Fatal("foreign or live lock was recovered")
			}
			content, err := os.ReadFile(lockFile)
			if err != nil || string(content) != test.content {
				t.Fatalf("lock changed: %v: %s", err, content)
			}
		})
	}
}

func TestStartStagesBacklogInPrivateDirectory(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	staged, _, err := stageBacklog(filepath.Join(root, "backlog.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(staged))
	if filepath.Dir(staged) == root {
		t.Fatal("backlog staged directly in the Tincan root")
	}
	info, err := os.Stat(filepath.Dir(staged))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("staging directory mode is %o", info.Mode().Perm())
	}
}
