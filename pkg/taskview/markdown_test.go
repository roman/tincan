package taskview

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/stateversion"
	"github.com/roman/tincan/pkg/taskstate"
)

func versionTwoRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	guard, err := stateversion.AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.PublishVersion(2); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestMarkdownShowsStateDependenciesAndHold(t *testing.T) {
	queued, err := taskstate.NewQueued(taskstate.Definition{
		ID: "task-a", Title: "Inspect", Body: "First line\nSecond line", Repo: "tincan", Kind: "audit",
		Dependencies: []taskstate.Dependency{{Type: "blocked-by", ID: "task-b", Reason: "review"}},
		Hold:         &taskstate.Hold{Reason: "Waiting", Kind: "captain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	done, err := taskstate.NewMigrated(taskstate.Snapshot{
		Definition: taskstate.Definition{
			ID: "task-b", Title: "Complete", Repo: "tincan", Kind: "ship", Closed: "2026-10-11",
		},
		State: taskstate.Done,
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteMarkdown(&output, []taskstate.Task{queued, done}); err != nil {
		t.Fatal(err)
	}
	want := "# Backlog\n\n## In flight\n## Queued\n" +
		"- [ ] task-a - Inspect (repo: tincan) (kind: audit) " +
		"(hold: Waiting) (hold-kind: captain) blocked-by: task-b - review\n" +
		"  First line\n  Second line\n" +
		"## Done\n- [x] task-b - Complete (repo: tincan) (kind: ship) (done 2026-10-11)\n"
	if output.String() != want {
		t.Fatalf("Markdown = %q, want %q", output.String(), want)
	}
}

func TestMarkdownShowsInFlightPriorityAndDates(t *testing.T) {
	priority := 1
	task, err := taskstate.NewMigrated(taskstate.Snapshot{
		Definition: taskstate.Definition{
			ID: "task-running", Title: "Build", Repo: "tincan", Kind: "ship",
			Priority: &priority, Created: "2026-10-10",
			Hold: &taskstate.Hold{Reason: "Review", Kind: "captain", Until: "2026-10-12"},
		},
		State: taskstate.InFlight,
		Worker: &taskstate.Worker{
			Name: "worker-1", Harness: "codex", Session: "work", Workspace: "workspace-1",
			Tab: "tab-1", Pane: "pane-1", Project: "/project", Worktree: "/worktree",
			Generation: "gen-1", AgentArgs: []string{"resume"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteMarkdown(&output, []taskstate.Task{task}); err != nil {
		t.Fatal(err)
	}
	want := "## In flight\n- **task-running** - Build (repo: tincan) (kind: ship) " +
		"(priority: 1) (since 2026-10-10) (hold: Review) " +
		"(hold-kind: captain) (hold-until: 2026-10-12)\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("in-flight view = %s", output.String())
	}
}

func TestRebuildReplacesStaleProjectionFromRecords(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	task, err := taskstate.NewQueued(taskstate.Definition{
		ID: "task-a", Title: "Current task", Repo: "tincan", Kind: "ship",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "backlog.md")
	if err := os.WriteFile(path, []byte("stale projection\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "task-a - Current task") ||
		strings.Contains(string(data), "stale projection") {
		t.Fatalf("rebuilt projection = %s", data)
	}
}

func TestRebuildRefusesVersionOneAndSymlinkedProjection(t *testing.T) {
	legacy := t.TempDir()
	if err := Rebuild(context.Background(), legacy); !errors.Is(err, stateversion.ErrIncompatible) {
		t.Fatalf("version 1 Rebuild error = %v", err)
	}
	root := versionTwoRoot(t)
	target := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(target, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "backlog.md")); err != nil {
		t.Fatal(err)
	}
	if err := Rebuild(context.Background(), root); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("symlinked Rebuild error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "preserve\n" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func TestRebuildReportsPublishedProjectionWhenDirectorySyncFails(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	task, err := taskstate.NewQueued(taskstate.Definition{
		ID: "task-a", Title: "Current task", Repo: "tincan", Kind: "ship",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("injected sync failure")
	err = rebuildWithSync(context.Background(), root, func(*os.Root) error { return syncFailure })
	if !errors.Is(err, ErrProjectionPublicationUnconfirmed) || !errors.Is(err, syncFailure) {
		t.Fatalf("Rebuild error = %v, want uncertain published projection", err)
	}
	path := filepath.Join(root, "backlog.md")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "task-a - Current task") {
		t.Fatalf("published projection = %q, %v", data, err)
	}
	if err := Rebuild(context.Background(), root); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestProjectionLockSerializesRebuilds(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- withProjectionLock(context.Background(), root, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first rebuild did not acquire the projection lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	secondEntered := false
	err = withProjectionLock(ctx, root, func() error {
		secondEntered = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || secondEntered {
		close(release)
		t.Fatalf(
			"second rebuild crossed the projection lock: entered=%t error=%v",
			secondEntered,
			err,
		)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
