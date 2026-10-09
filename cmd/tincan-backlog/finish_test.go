package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/herdr"
)

type finishRunner struct {
	response string
	during   func()
}

func (runner finishRunner) RunSession(
	_ context.Context, session string, arguments ...string,
) ([]byte, []byte, error) {
	if session != "work" || strings.Join(arguments, " ") != "pane get pane-1 --session work" {
		return nil, nil, errors.New("unexpected Herdr request")
	}
	if runner.during != nil {
		runner.during()
	}
	return []byte(runner.response), nil, nil
}

func finishCLI(t *testing.T, root, mode, response string) (int, string) {
	t.Helper()
	var output bytes.Buffer
	code := runWithClient(
		context.Background(),
		[]string{"--root", root, "finish", "task-1", "--mode", mode},
		&output,
		herdr.Client{SessionRunner: finishRunner{response: response}},
	)
	return code, output.String()
}

func TestFinishRequiresInFlightBoundTaskAndGonePane(t *testing.T) {
	for _, test := range []struct {
		name         string
		bind         bool
		start        bool
		paneResponse string
	}{
		{"queued without worker", false, false, `{"error":{"code":"pane_not_found"}}`},
		{"queued with worker", true, false, `{"error":{"code":"pane_not_found"}}`},
		{"live pane", true, true, `{"result":{"pane":{"pane_id":"pane-1"}}}`},
		{"unreadable pane", true, true, `{"error":{"code":"server_unavailable"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := backlogRoot(t)
			queueTask(t, root, "task-1")
			if test.bind {
				bindWorker(t, root, "task-1")
			}
			if test.start {
				startBoundTask(t, root)
			}
			code, output := finishCLI(t, root, "close", test.paneResponse)
			if code == 0 {
				t.Fatalf("unsafe completion succeeded: %s", output)
			}
			wantState := "state: queued"
			if test.start {
				wantState = "state: in_flight"
			}
			if !strings.Contains(backlogState(t, root, "task-1"), wantState) {
				t.Fatal("backlog state changed")
			}
			if test.bind {
				for _, path := range []string{
					filepath.Join(root, "tasks", "task-1.meta"),
					filepath.Join(root, "crew", "task-1.json"),
				} {
					if _, err := os.Lstat(path); err != nil {
						t.Fatalf("task record changed at %s: %v", path, err)
					}
				}
			}
		})
	}
}

func startBoundTask(t *testing.T, root string) {
	t.Helper()
	var output bytes.Buffer
	if code := run([]string{"--root", root, "start", "task-1"}, &output); code != 0 {
		t.Fatalf("start task: %s", output.String())
	}
}

func TestFinishClosesOrRetainsBoundTask(t *testing.T) {
	for _, test := range []struct {
		mode      string
		held      bool
		wantState string
	}{
		{"close", false, "state: done"},
		{"retain", true, "state: queued"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			root := backlogRoot(t)
			queueTask(t, root, "task-1")
			bindWorker(t, root, "task-1")
			startBoundTask(t, root)
			if test.held {
				output, err := exec.Command(
					"tasks-axi", "hold", "task-1", "--reason", "Awaiting answer",
					"--file", filepath.Join(root, "backlog.md"),
				).CombinedOutput()
				if err != nil {
					t.Fatalf("hold task: %v: %s", err, output)
				}
			}
			code, output := finishCLI(t, root, test.mode, `{"error":{"code":"pane_not_found"}}`)
			if code != 0 {
				t.Fatalf("finish task: %s", output)
			}
			row := backlogState(t, root, "task-1")
			if !strings.Contains(row, test.wantState) {
				t.Fatalf("wrong backlog state: %s", row)
			}
			if test.held && !strings.Contains(row, "held: yes") {
				t.Fatalf("retention lost the hold: %s", row)
			}
			for _, path := range []string{
				filepath.Join(root, "tasks", "task-1.meta"),
				filepath.Join(root, "crew", "task-1.json"),
				filepath.Join(root, "tasks", "task-1.completion.json"),
			} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("record remains at %s: %v", path, err)
				}
			}
		})
	}
}

func TestFinishRespectsHeldRow(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
		held bool
	}{
		{"held close", "close", true},
		{"unheld retain", "retain", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := backlogRoot(t)
			queueTask(t, root, "task-1")
			bindWorker(t, root, "task-1")
			startBoundTask(t, root)
			if test.held {
				output, err := exec.Command(
					"tasks-axi", "hold", "task-1", "--reason", "Awaiting answer",
					"--file", filepath.Join(root, "backlog.md"),
				).CombinedOutput()
				if err != nil {
					t.Fatalf("hold task: %v: %s", err, output)
				}
			}
			if code, output := finishCLI(
				t, root, test.mode, `{"error":{"code":"pane_not_found"}}`,
			); code == 0 {
				t.Fatalf("invalid held transition succeeded: %s", output)
			}
			if !strings.Contains(backlogState(t, root, "task-1"), "state: in_flight") {
				t.Fatal("backlog row changed")
			}
			for _, path := range []string{
				filepath.Join(root, "tasks", "task-1.meta"),
				filepath.Join(root, "crew", "task-1.json"),
			} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("task record changed at %s: %v", path, err)
				}
			}
		})
	}
}

func retireBeforeBacklog(t *testing.T, root string) {
	t.Helper()
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCompletion("task-1", func(completion *crew.Completion) error {
		return completion.Retire(
			context.Background(),
			herdr.Client{SessionRunner: finishRunner{response: `{"error":{"code":"pane_not_found"}}`}},
			crew.CloseTask,
		)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFinishResumesPendingClose(t *testing.T) {
	for _, test := range []struct {
		name        string
		moveBacklog bool
	}{
		{"records retired", false},
		{"backlog already done", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := backlogRoot(t)
			queueTask(t, root, "task-1")
			bindWorker(t, root, "task-1")
			startBoundTask(t, root)
			retireBeforeBacklog(t, root)
			if test.moveBacklog {
				output, err := exec.Command(
					"tasks-axi", "done", "task-1", "--no-prune",
					"--file", filepath.Join(root, "backlog.md"),
				).CombinedOutput()
				if err != nil {
					t.Fatalf("done task: %v: %s", err, output)
				}
			}
			code, output := finishCLI(t, root, "close", "")
			if code != 0 {
				t.Fatalf("retry pending close: %s", output)
			}
			if test.moveBacklog != strings.Contains(output, "already: true") {
				t.Fatalf("wrong retry result: %s", output)
			}
			if !strings.Contains(backlogState(t, root, "task-1"), "state: done") {
				t.Fatal("backlog row remains in flight")
			}
			if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.completion.json")); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				t.Fatalf("pending marker remains: %v", err)
			}
		})
	}
}

func TestFinishRefusesBacklogLockBeforeRetirement(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	unlock, err := lockBacklog(filepath.Join(root, "backlog.md"))
	if err != nil {
		t.Fatal(err)
	}
	code, output := finishCLI(t, root, "close", `{"error":{"code":"pane_not_found"}}`)
	if code == 0 {
		t.Fatalf("locked backlog changed: %s", output)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, "tasks", "task-1.meta"),
		filepath.Join(root, "crew", "task-1.json"),
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("record changed while backlog locked: %v", err)
		}
	}
}

func TestFinishRetainsMarkerWhenBacklogUnlockFails(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	lockPath := filepath.Join(root, "backlog.md.lock")
	runner := finishRunner{
		response: `{"error":{"code":"pane_not_found"}}`,
		during: func() {
			if err := os.Rename(lockPath, lockPath+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(lockPath, []byte("other owner\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	var output bytes.Buffer
	code := runWithClient(
		context.Background(),
		[]string{"--root", root, "finish", "task-1", "--mode", "close"},
		&output,
		herdr.Client{SessionRunner: runner},
	)
	if code == 0 || !strings.Contains(output.String(), "backlog lock changed") {
		t.Fatalf("unlock failure was hidden: %s", output.String())
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: done") {
		t.Fatal("backlog transition did not land before unlock failure")
	}
	markerPath := filepath.Join(root, "tasks", "task-1.completion.json")
	if _, err := os.Lstat(markerPath); err != nil {
		t.Fatalf("pending marker was lost: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if code, output := finishCLI(t, root, "close", ""); code != 0 ||
		!strings.Contains(output, "already: true") {
		t.Fatalf("pending close retry failed: %s", output)
	}
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending marker remains after retry: %v", err)
	}
}
