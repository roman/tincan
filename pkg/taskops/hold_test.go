package taskops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestHoldReleaseAndRetainLifecycle(t *testing.T) {
	service, _, _ := runningWorkerService(t)
	ctx := context.Background()
	hold := taskstate.Hold{Reason: "Review changes", Kind: "captain", Until: "2026-10-15"}
	held, err := service.PlaceHold(ctx, "task-1", hold)
	if err != nil || held.Definition().Hold == nil || *held.Definition().Hold != hold {
		t.Fatalf("hold: %#v %v", held.Definition(), err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); !errors.Is(
		err,
		taskstate.ErrInvalidTransition,
	) {
		t.Fatalf("held close: %v", err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Retain, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReleaseHold(ctx, "task-1"); !errors.Is(
		err,
		taskstate.ErrInvalidTransition,
	) {
		t.Fatalf("release before retirement: %v", err)
	}
	client := herdr.Client{
		SessionRunner: &retirementRunner{response: `{"error":{"code":"pane_not_found"}}`},
	}
	if _, err := service.Retire(ctx, "task-1", "gen-1", client); err != nil {
		t.Fatal(err)
	}
	worker := taskstate.Worker{
		Name:       "worker-2",
		Harness:    "codex",
		Session:    "work",
		Pane:       "pane-2",
		Project:    "/project",
		Worktree:   "/worktree",
		Generation: "gen-2",
		AgentArgs:  []string{"resume", "thread-2"},
	}
	adoptStoredWorker(t, service.store, "task-1", worker, "tab-2")
	if _, err := service.Start(ctx, "task-1"); !errors.Is(err, taskstate.ErrInvalidTransition) {
		t.Fatalf("held start: %v", err)
	}
	for range 2 {
		released, err := service.ReleaseHold(ctx, "task-1")
		if err != nil || released.Definition().Hold != nil {
			t.Fatalf("release: %#v %v", released.Definition(), err)
		}
	}
	if _, err := service.Start(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-2", taskstate.Close, "2026-10-12"); err != nil {
		t.Fatal(err)
	}
}

func TestHoldOperationsPreserveMutationAndRepairProjection(t *testing.T) {
	for _, action := range []string{"hold", "release"} {
		t.Run(action, func(t *testing.T) {
			service, _, _ := runningWorkerService(t)
			root := service.root
			ctx := context.Background()
			hold := taskstate.Hold{Reason: "Waiting", Kind: "captain"}
			if action == "release" {
				if _, err := service.PlaceHold(ctx, "task-1", hold); err != nil {
					t.Fatal(err)
				}
			}
			projection := filepath.Join(root, "backlog.md")
			if err := os.Remove(projection); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(projection, 0700); err != nil {
				t.Fatal(err)
			}
			mutate := func() (taskstate.Task, error) { return service.PlaceHold(ctx, "task-1", hold) }
			if action == "release" {
				mutate = func() (taskstate.Task, error) { return service.ReleaseHold(ctx, "task-1") }
			}
			updated, err := mutate()
			if err == nil || updated.ID() != "task-1" {
				t.Fatalf("projection failure: %#v %v", updated.Snapshot(), err)
			}
			stored, err := service.store.Read(ctx, "task-1")
			if err != nil || (stored.Definition().Hold != nil) != (action == "hold") {
				t.Fatalf("saved mutation: %#v %v", stored.Definition(), err)
			}
			if err := os.Remove(projection); err != nil {
				t.Fatal(err)
			}
			if _, err := mutate(); err != nil {
				t.Fatalf("repair retry: %v", err)
			}
			data, err := os.ReadFile(projection)
			if err != nil ||
				strings.Contains(string(data), "(hold: Waiting)") != (action == "hold") {
				t.Fatalf("projection: %s %v", data, err)
			}
		})
	}
}
