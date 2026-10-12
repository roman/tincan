package taskops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/stateversion"
	"github.com/roman/tincan/pkg/taskstate"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	migration, err := stateversion.AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.PublishVersion(2); err != nil {
		t.Fatal(err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCreateAndFinishKeepRecordAndBacklogViewTogether(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	definition := taskstate.Definition{
		ID:    "task-1",
		Title: "Ship work",
		Repo:  "exmachina",
		Kind:  "worker",
	}
	if _, err := service.Create(ctx, definition); err != nil {
		t.Fatal(err)
	}
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		reserved, err := task.ReserveAdoption(worker)
		if err != nil {
			return taskstate.Task{}, err
		}
		return reserved.ConfirmAdoption("workspace-1", "tab-1")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(ctx, "task-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Read(ctx, "task-1")
	if err != nil || stored.State() != taskstate.Done {
		t.Fatalf("record = %#v, %v", stored.Snapshot(), err)
	}
	view, err := os.ReadFile(filepath.Join(root, "backlog.md"))
	if err != nil || !strings.Contains(string(view), "- [x] task-1 - Ship work") {
		t.Fatalf("backlog view = %q, %v", view, err)
	}
}

func TestStartRequiresFinishedDependencies(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, taskstate.Definition{
		ID: "task-1", Title: "First", Repo: "exmachina", Kind: "worker",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, taskstate.Definition{
		ID: "task-2", Title: "Second", Repo: "exmachina", Kind: "worker",
		Dependencies: []taskstate.Dependency{{Type: "blocked-by", ID: "task-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(ctx, "task-2"); !errors.Is(err, ErrBlockedDependency) {
		t.Fatalf("start with queued dependency = %v", err)
	}
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		snapshot := task.Snapshot()
		snapshot.State = taskstate.Done
		return taskstate.NewMigrated(snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	worker := taskstate.Worker{
		Name: "worker-2", Harness: "codex", Session: "work", Pane: "pane-2",
		Project: "/project", Worktree: "/worktree", Generation: "gen-2",
		AgentArgs: []string{"resume", "thread-2"},
	}
	if _, err := store.Update(ctx, "task-2", func(task taskstate.Task) (taskstate.Task, error) {
		reserved, err := task.ReserveAdoption(worker)
		if err != nil {
			return taskstate.Task{}, err
		}
		return reserved.ConfirmAdoption("workspace-1", "tab-2")
	}); err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, "task-2")
	if err != nil || started.State() != taskstate.InFlight {
		t.Fatalf("start after dependency completion = %#v, %v", started.Snapshot(), err)
	}
}

func TestCreateRetryRebuildsFailedProjection(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	service, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	definition := taskstate.Definition{
		ID:    "task-1",
		Title: "Work",
		Repo:  "exmachina",
		Kind:  "worker",
	}
	projection := filepath.Join(root, "backlog.md")
	if err := os.Mkdir(projection, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, definition); err == nil {
		t.Fatal("create succeeded with invalid projection target")
	}
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, definition); err != nil {
		t.Fatalf("create retry failed: %v", err)
	}
	view, err := os.ReadFile(projection)
	if err != nil || !strings.Contains(string(view), "task-1 - Work") {
		t.Fatalf("rebuilt view = %q, %v", view, err)
	}
	changed := definition
	changed.Title = "Different work"
	if _, err := service.Create(ctx, changed); !errors.Is(err, os.ErrExist) {
		t.Fatalf("conflicting create = %v", err)
	}
}

func TestStartReportsSavedRecordWhenProjectionFails(t *testing.T) {
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
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	worker := taskstate.Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Pane: "pane-1",
		Project: "/project", Worktree: "/worktree", Generation: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
	if _, err := store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		reserved, err := task.ReserveAdoption(worker)
		if err != nil {
			return taskstate.Task{}, err
		}
		return reserved.ConfirmAdoption("workspace-1", "tab-1")
	}); err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(root, "backlog.md")
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(projection, 0o700); err != nil {
		t.Fatal(err)
	}
	updated, err := service.Start(ctx, "task-1")
	if err == nil || updated.State() != taskstate.InFlight {
		t.Fatalf("start with invalid view = %#v, %v", updated.Snapshot(), err)
	}
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(ctx, "task-1"); err != nil {
		t.Fatalf("start retry failed: %v", err)
	}
	view, err := os.ReadFile(projection)
	if err != nil || !strings.Contains(string(view), "**task-1** - Work") {
		t.Fatalf("rebuilt view = %q, %v", view, err)
	}
}
