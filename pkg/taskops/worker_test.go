package taskops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/supervise"
	"github.com/roman/tincan/pkg/taskstate"
)

type workerRunner struct {
	worktree    string
	harness     string
	absent      bool
	unreadable  bool
	promptError error
	prompts     int
	calls       int
	onPrompt    func()
}

func (runner *workerRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls++
	if session != "work" {
		return nil, nil, fmt.Errorf("unexpected session %s", session)
	}
	switch strings.Join(args[:2], " ") {
	case "pane get":
		return []byte(
			fmt.Sprintf(
				`{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":%q}}}`,
				runner.worktree,
			),
		), nil, nil
	case "agent get":
		if runner.absent {
			return nil, []byte(`{"error":{"code":"agent_not_found"}}`), errors.New("exit 1")
		}
		return []byte(
			fmt.Sprintf(
				`{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":%q}}}`,
				runner.harness,
			),
		), nil, nil
	case "pane process-info":
		if runner.unreadable {
			return []byte(`{"result":{}}`), nil, nil
		}
		return []byte(
			`{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}`,
		), nil, nil
	case "agent prompt":
		runner.prompts++
		if runner.onPrompt != nil {
			runner.onPrompt()
		}
		return []byte(`{"result":{}}`), nil, runner.promptError
	default:
		return nil, nil, fmt.Errorf("unexpected command %v", args)
	}
}

func runningWorkerService(t *testing.T) (*Service, *workerRunner, string) {
	t.Helper()
	ctx := context.Background()
	service, err := New(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, taskstate.Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "worker"}); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	adoptStoredWorker(t, service.store, "task-1", taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1", Project: service.root,
		Worktree: worktree, Generation: "gen-1", AgentArgs: []string{"resume", "thread-1"},
	}, "tab-1")
	if _, err := service.Start(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	return service, &workerRunner{
			worktree: worktree,
			harness:  "codex",
		}, filepath.Join(
			t.TempDir(),
			"messages",
		)
}

func TestWorkerObservationRejectsInvalidBindingAndCompletedTask(t *testing.T) {
	for _, test := range []string{"wrong directory", "wrong harness", "completed", "missing", "unreadable"} {
		t.Run(test, func(t *testing.T) {
			service, runner, messages := runningWorkerService(t)
			switch test {
			case "wrong directory":
				runner.worktree = t.TempDir()
			case "wrong harness":
				runner.harness = "claude"
			case "completed":
				if _, err := service.Finish(context.Background(), "task-1", "gen-1", taskstate.Close, "2026-10-11"); err != nil {
					t.Fatal(err)
				}
			case "missing":
				runner.absent = true
			case "unreadable":
				runner.unreadable = true
			}
			worker, err := service.SupervisedWorker(
				"task-1",
				messages,
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			err = worker.Observe(context.Background())
			if err == nil || (test == "missing" && !errors.Is(err, supervise.ErrMissing)) {
				t.Fatalf("Observe = %v", err)
			}
			if runner.prompts != 0 || (test == "completed" && runner.calls != 0) {
				t.Fatalf("unexpected external actions: %#v", runner)
			}
		})
	}
}

func TestWorkerObservationRecoversGenerationAndBootstrapPrompt(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	ctx := context.Background()
	if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		snapshot := task.Snapshot()
		snapshot.Worker.PendingGeneration = "gen-2"
		return taskstate.NewMigrated(snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	store, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	ids := make(map[string]inbox.MessageID)
	for _, generation := range []string{"gen-1", "gen-2"} {
		body, _ := json.Marshal(
			map[string]string{
				"kind":             "task-bootstrap",
				"state":            filepath.Join(service.root, "tasks"),
				"task":             "task-1",
				"spawn_generation": generation,
			},
		)
		message, err := store.Send(recipient, generation, string(body))
		if err != nil {
			t.Fatal(err)
		}
		ids[generation] = message.ID
	}
	unrelated, err := store.Send(recipient, "unrelated", "Keep this message")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	runner.promptError = errors.New("prompt failed")
	if err := worker.Observe(ctx); err == nil {
		t.Fatal("prompt failure was lost")
	}
	runner.promptError = nil
	if err := worker.Observe(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := service.store.Read(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	bound := current.Snapshot().Worker
	if bound.Generation != "gen-2" || bound.PendingGeneration != "" || bound.PendingLaunch != "" {
		t.Fatalf("worker = %#v", bound)
	}
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 2 || runner.prompts != 2 {
		t.Fatalf("pending = %#v, %v; prompts = %d", pending, err, runner.prompts)
	}
	for _, id := range []inbox.MessageID{ids["gen-2"], unrelated.ID} {
		if _, err := store.Read(recipient, id); err != nil {
			t.Errorf("expected pending message %s: %v", id, err)
		}
	}
	if _, err := store.ReadHandled(recipient, ids["gen-1"]); err != nil {
		t.Errorf("stale bootstrap was not handled: %v", err)
	}
	if _, err := store.Read(recipient, ids["gen-1"]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale bootstrap remains pending: %v", err)
	}
}

func TestWorkerObservationHoldsTaskLockThroughPrompt(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	ctx := context.Background()
	if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		return task.ReserveLaunch("gen-2")
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{
		"kind": "task-bootstrap", "state": filepath.Join(service.root, "tasks"),
		"task": "task-1", "spawn_generation": "gen-1",
	})
	if _, err := worker.messages.Send(worker.recipient, "current", string(body)); err != nil {
		t.Fatal(err)
	}
	runner.onPrompt = func() {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		_, err := service.store.Update(
			ctx,
			"task-1",
			func(task taskstate.Task) (taskstate.Task, error) {
				return task.AcceptResult("gen-1", taskstate.Close, "2026-10-11")
			},
		)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("completion during prompt = %v", err)
		}
	}
	if err := worker.Observe(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := service.store.Read(ctx, "task-1")
	if err != nil || current.Snapshot().Worker.PendingLaunch != "" || runner.prompts != 1 {
		t.Fatalf("reconciled task = %#v, %v; prompts = %d", current.Snapshot(), err, runner.prompts)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-11"); err != nil {
		t.Fatalf("completion after observation = %v", err)
	}
}
