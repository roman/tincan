package taskops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/taskstate"
)

func TestAdoptionReservationAndConfirmation(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, taskstate.Definition{
		ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "worker",
	}); err != nil {
		t.Fatal(err)
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
	for _, operation := range []struct {
		name        string
		run         func() (taskstate.Task, error)
		wantPending bool
	}{
		{name: "reserve", run: func() (taskstate.Task, error) {
			return service.ReserveAdoption(ctx, "task-1", worker)
		}, wantPending: true},
		{name: "retry reservation", run: func() (taskstate.Task, error) {
			return service.ReserveAdoption(ctx, "task-1", worker)
		}, wantPending: true},
		{name: "confirm", run: func() (taskstate.Task, error) {
			return service.ConfirmAdoption(ctx, "task-1", "workspace-1", "tab-1")
		}},
		{name: "retry confirmation", run: func() (taskstate.Task, error) {
			return service.ConfirmAdoption(ctx, "task-1", "workspace-1", "tab-1")
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			updated, err := operation.run()
			if err != nil {
				t.Fatal(err)
			}
			got := updated.Snapshot().Worker
			if got == nil || got.PendingAdoption != operation.wantPending {
				t.Fatalf("worker = %#v", got)
			}
		})
	}
	if _, err := service.Start(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	view, err := os.ReadFile(filepath.Join(root, "backlog.md"))
	if err != nil || !strings.Contains(string(view), "**task-1** - Work") {
		t.Fatalf("backlog view = %q, %v", view, err)
	}
}

func TestCancelAdoptionReleasesOnlyPendingWorker(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, taskstate.Definition{
		ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "worker",
	}); err != nil {
		t.Fatal(err)
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
	if _, err := service.ReserveAdoption(ctx, "task-1", worker); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		updated, err := service.CancelAdoption(ctx, "task-1")
		if err != nil || updated.Snapshot().Worker != nil {
			t.Fatalf("cancel = %#v, %v", updated.Snapshot(), err)
		}
	}
	if _, err := service.ReserveAdoption(ctx, "task-1", worker); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmAdoption(ctx, "task-1", "workspace-1", "tab-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelAdoption(ctx, "task-1"); !errors.Is(
		err,
		taskstate.ErrInvalidTransition,
	) {
		t.Fatalf("cancel confirmed worker = %v", err)
	}
}

func TestReserveAdoptionRejectsSharedWorkerIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*taskstate.Worker)
	}{
		{name: "same name", change: func(worker *taskstate.Worker) { worker.Pane = "pane-2" }},
		{name: "same pane", change: func(worker *taskstate.Worker) { worker.Name = "worker-2" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			service, err := New(testRoot(t))
			if err != nil {
				t.Fatal(err)
			}
			for _, taskID := range []string{"task-1", "task-2"} {
				if _, err := service.Create(ctx, taskstate.Definition{
					ID: taskID, Title: taskID, Repo: "exmachina", Kind: "worker",
				}); err != nil {
					t.Fatal(err)
				}
			}
			first := taskstate.Worker{
				Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
				Project: "/project", Worktree: "/worktree", Generation: "gen-1",
				AgentArgs: []string{"resume", "thread-1"},
			}
			second := first
			second.Generation = "gen-2"
			test.change(&second)
			if _, err := service.ReserveAdoption(ctx, "task-1", first); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ReserveAdoption(ctx, "task-2", second); !errors.Is(
				err, taskstate.ErrWorkerConflict,
			) {
				t.Fatalf("reservation conflict = %v", err)
			}
		})
	}
}

func TestGenericUpdateCannotDuplicateWorkerIdentity(t *testing.T) {
	ctx := context.Background()
	service, err := New(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"task-1", "task-2"} {
		if _, err := service.Create(ctx, taskstate.Definition{
			ID: taskID, Title: taskID, Repo: "exmachina", Kind: "worker",
		}); err != nil {
			t.Fatal(err)
		}
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
	if _, err := service.ReserveAdoption(ctx, "task-1", worker); err != nil {
		t.Fatal(err)
	}
	worker.Generation = "gen-2"
	_, err = service.store.Update(ctx, "task-2", func(task taskstate.Task) (taskstate.Task, error) {
		return task.ReserveAdoption(worker)
	})
	if !errors.Is(err, taskstate.ErrWorkerConflict) {
		t.Fatalf("generic update conflict = %v", err)
	}
}

func TestCreateRejectsWorkerBearingTask(t *testing.T) {
	ctx := context.Background()
	service, err := New(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	definition := taskstate.Definition{
		ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "worker",
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		PendingAdoption: true,
		AgentArgs:       []string{"resume", "thread-1"},
	}
	task, err := taskstate.NewMigrated(taskstate.Snapshot{
		Definition: definition, State: taskstate.Queued, Worker: &worker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.store.Create(ctx, task); !errors.Is(err, taskstate.ErrInvalidTask) {
		t.Fatalf("create worker-bearing task = %v", err)
	}
}

func TestWorkerIdentityCanBeReusedInAnotherSession(t *testing.T) {
	ctx := context.Background()
	service, err := New(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"task-1", "task-2"} {
		if _, err := service.Create(ctx, taskstate.Definition{
			ID: taskID, Title: taskID, Repo: "exmachina", Kind: "worker",
		}); err != nil {
			t.Fatal(err)
		}
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work-1", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
	if _, err := service.ReserveAdoption(ctx, "task-1", worker); err != nil {
		t.Fatal(err)
	}
	worker.Session = "work-2"
	worker.Generation = "gen-2"
	if _, err := service.ReserveAdoption(ctx, "task-2", worker); err != nil {
		t.Fatalf("other Herdr session = %v", err)
	}
}

func TestWorkerIdentityCheckHoldsAcrossTaskUpdate(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"task-1", "task-2"} {
		if _, err := service.Create(ctx, taskstate.Definition{
			ID: taskID, Title: taskID, Repo: "exmachina", Kind: "worker",
		}); err != nil {
			t.Fatal(err)
		}
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
	taskLock, err := os.OpenFile(
		filepath.Join(root, ".task-task-1.lock"),
		os.O_CREATE|os.O_RDWR,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer taskLock.Close()
	if err := syscall.Flock(int(taskLock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.ReserveAdoption(ctx, "task-1", worker)
		firstDone <- err
	}()
	workerLock, err := os.OpenFile(
		filepath.Join(root, ".worker-adoption.lock"),
		os.O_CREATE|os.O_RDWR,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer workerLock.Close()
	deadline := time.After(time.Second)
	for {
		err := syscall.Flock(int(workerLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(workerLock.Fd()), syscall.LOCK_UN); err != nil {
			t.Fatal(err)
		}
		select {
		case <-deadline:
			t.Fatal("first reservation did not reach the worker identity lock")
		case <-time.After(time.Millisecond):
		}
	}
	secondDone := make(chan error, 1)
	go func() {
		other := worker
		other.Generation = "gen-2"
		_, err := service.ReserveAdoption(ctx, "task-2", other)
		secondDone <- err
	}()
	unrelatedDone := make(chan error, 1)
	go func() {
		_, err := service.store.Update(ctx, "task-2", taskstate.Task.ReleaseHold)
		unrelatedDone <- err
	}()
	select {
	case err := <-unrelatedDone:
		if err != nil {
			t.Fatalf("unrelated update = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated update waited for worker identity")
	}
	select {
	case err := <-secondDone:
		t.Fatalf("second reservation crossed the first claim: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := syscall.Flock(int(taskLock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; !errors.Is(err, taskstate.ErrWorkerConflict) {
		t.Fatalf("second reservation = %v", err)
	}
}
