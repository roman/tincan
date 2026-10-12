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
}

func (runner finishRunner) RunSession(
	_ context.Context, session string, arguments ...string,
) ([]byte, []byte, error) {
	if session != "work" || strings.Join(arguments, " ") != "pane get pane-1 --session work" {
		return nil, nil, errors.New("unexpected Herdr request")
	}
	return []byte(runner.response), nil, nil
}

func finishCLI(t *testing.T, root, mode string) (int, string) {
	t.Helper()
	var output bytes.Buffer
	code := runWithClient(
		context.Background(),
		[]string{"--root", root, "finish", "task-1", "--mode", mode},
		&output,
		herdr.Client{SessionRunner: finishRunner{}},
	)
	return code, output.String()
}

func TestFinishRecordsResultBeforePaneCleanup(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	livePane := `{"result":{"pane":{"pane_id":"pane-1"}}}`
	if code, output := finishCLI(t, root, "close"); code != 0 {
		t.Fatalf("finish with live pane: %s", output)
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: done") {
		t.Fatal("accepted result did not reach the backlog")
	}
	for _, path := range []string{
		filepath.Join(root, "tasks", "task-1.meta"),
		filepath.Join(root, "crew", "task-1.json"),
		filepath.Join(root, "tasks", "task-1.completion.json"),
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("finish removed %s before cleanup: %v", path, err)
		}
	}
	cleanup := func(response string) (int, string) {
		var output bytes.Buffer
		code := runWithClient(context.Background(),
			[]string{"--root", root, "cleanup", "task-1"}, &output,
			herdr.Client{SessionRunner: finishRunner{response: response}})
		return code, output.String()
	}
	if code, _ := cleanup(livePane); code == 0 {
		t.Fatal("cleanup retired a live pane")
	}
	if code, output := cleanup(`{"error":{"code":"pane_not_found"}}`); code != 0 {
		t.Fatalf("cleanup after pane departure: %s", output)
	}
	for _, path := range []string{
		filepath.Join(root, "tasks", "task-1.meta"),
		filepath.Join(root, "crew", "task-1.json"),
		filepath.Join(root, "tasks", "task-1.completion.json"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cleanup left %s: %v", path, err)
		}
	}
}

func TestFinishResumesAcceptedResultBeforeBacklogTransition(t *testing.T) {
	root := backlogRoot(t)
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCompletion("task-1", func(completion *crew.Completion) error {
		return completion.Accept(crew.CloseTask)
	}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := runWithClient(context.Background(),
		[]string{"--root", root, "cleanup", "task-1"}, &output,
		herdr.Client{SessionRunner: finishRunner{response: `{"error":{"code":"pane_not_found"}}`}},
	); code == 0 {
		t.Fatal("cleanup accepted an in-flight backlog row")
	}
	if code, result := finishCLI(t, root, "close"); code != 0 {
		t.Fatalf("finish retry: %s", result)
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: done") {
		t.Fatal("backlog transition was lost")
	}
	if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.completion.json")); err != nil {
		t.Fatalf("accepted result was lost: %v", err)
	}
}

func TestFinishRequiresInFlightBoundTask(t *testing.T) {
	for _, test := range []struct {
		name string
		bind bool
	}{
		{"queued without worker", false},
		{"queued with worker", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := backlogRoot(t)
			queueTask(t, root, "task-1")
			if test.bind {
				bindWorker(t, root, "task-1")
			}
			code, output := finishCLI(t, root, "close")
			if code == 0 {
				t.Fatalf("unsafe completion succeeded: %s", output)
			}
			if !strings.Contains(backlogState(t, root, "task-1"), "state: queued") {
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
			code, output := finishCLI(t, root, test.mode)
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
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("accepted result lost %s: %v", path, err)
				}
			}
			var cleanupOutput bytes.Buffer
			if code := runWithClient(context.Background(),
				[]string{"--root", root, "cleanup", "task-1"}, &cleanupOutput,
				herdr.Client{SessionRunner: finishRunner{response: `{"error":{"code":"pane_not_found"}}`}},
			); code != 0 {
				t.Fatalf("cleanup: %s", cleanupOutput.String())
			}
			for _, path := range []string{
				filepath.Join(root, "tasks", "task-1.meta"),
				filepath.Join(root, "crew", "task-1.json"),
				filepath.Join(root, "tasks", "task-1.completion.json"),
			} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("cleanup left %s: %v", path, err)
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
			if code, output := finishCLI(t, root, test.mode); code == 0 {
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
			code, output := finishCLI(t, root, "close")
			if code != 0 {
				t.Fatalf("retry pending close: %s", output)
			}
			if test.moveBacklog != strings.Contains(output, "already: true") {
				t.Fatalf("wrong retry result: %s", output)
			}
			if !strings.Contains(backlogState(t, root, "task-1"), "state: done") {
				t.Fatal("backlog row remains in flight")
			}
			markerPath := filepath.Join(root, "tasks", "task-1.completion.json")
			if _, err := os.Lstat(markerPath); err != nil {
				t.Fatalf("accepted marker lost: %v", err)
			}
			var cleanupOutput bytes.Buffer
			if code := runWithClient(context.Background(),
				[]string{"--root", root, "cleanup", "task-1"}, &cleanupOutput,
				herdr.Client{SessionRunner: finishRunner{response: `{"error":{"code":"pane_not_found"}}`}},
			); code != 0 {
				t.Fatalf("cleanup: %s", cleanupOutput.String())
			}
			if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cleanup left marker: %v", err)
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
	code, output := finishCLI(t, root, "close")
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
