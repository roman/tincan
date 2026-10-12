package taskstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/stateversion"
)

func versionTwoRoot(t *testing.T) string {
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

func queuedStoreTask(t *testing.T) Task {
	t.Helper()
	task, err := NewQueued(Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestStoreRefusesVersionOneBeforeCreatingRecord(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); !errors.Is(
		err,
		stateversion.ErrIncompatible,
	) {
		t.Fatalf("Create error = %v, want version refusal", err)
	}
	if _, err := os.Stat(filepath.Join(root, "records", "task-1.json")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("version 1 state changed: %v", err)
	}
}

func TestStoreCreatesReadsAndUpdatesOneTask(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate Create error = %v, want exists", err)
	}
	updated, err := store.Update(context.Background(), "task-1", func(current Task) (Task, error) {
		snapshot := current.Snapshot()
		snapshot.Definition.Body = "Accepted work\n"
		return NewMigrated(snapshot)
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Definition().Body != "Accepted work\n" {
		t.Fatalf("updated task body = %q", updated.Definition().Body)
	}
	loaded, err := store.Read(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Definition().Body != "Accepted work\n" {
		t.Fatalf("stored task body = %q", loaded.Definition().Body)
	}
}

func TestStoreSerializesConcurrentUpdates(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	task := queuedStoreTask(t)
	if err := store.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	const updates = 12
	var workers sync.WaitGroup
	results := make(chan error, updates)
	for range updates {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := store.Update(
				context.Background(),
				task.ID(),
				func(current Task) (Task, error) {
					value, err := strconv.Atoi(current.Definition().Body)
					if current.Definition().Body == "" {
						value, err = 0, nil
					}
					if err != nil {
						return Task{}, err
					}
					snapshot := current.Snapshot()
					snapshot.Definition.Body = strconv.Itoa(value + 1)
					return NewMigrated(snapshot)
				},
			)
			results <- err
		}()
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Read(context.Background(), task.ID())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Definition().Body != strconv.Itoa(updates) {
		t.Fatalf("concurrent updates lost a change: %q", loaded.Definition().Body)
	}
}

func TestStoreUpdateKeepsVersionLeaseUntilPublish(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := store.Update(context.Background(), "task-1", func(task Task) (Task, error) {
			close(started)
			<-release
			return task, nil
		})
		finished <- err
	}()
	select {
	case <-started:
	case err := <-finished:
		t.Fatalf("Update ended before its callback: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Update did not enter its callback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := stateversion.AcquireMigration(ctx, root); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		close(release)
		t.Fatalf("migration crossed active version 2 writer: %v", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestStoreReadRejectsSymlinkedTaskRecord(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(root, "records", "task-1.json")
	if err := os.Rename(record, filepath.Join(root, "records", "target")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), "task-1"); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Read error = %v, want invalid record", err)
	}
}

func TestStorePreservesOriginalTaskWhenTransitionChangesID(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); err != nil {
		t.Fatal(err)
	}
	returned, err := store.Update(context.Background(), "task-1", func(Task) (Task, error) {
		return NewQueued(
			Definition{ID: "task-2", Title: "Different", Repo: "exmachina", Kind: "ship"},
		)
	})
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Update error = %v, want invalid task", err)
	}
	if returned.ID() != "" {
		t.Fatalf("Update returned unpublished task: %#v", returned)
	}
	loaded, err := store.Read(context.Background(), "task-1")
	if err != nil || loaded.Definition().Title != "Work" {
		t.Fatalf("original task changed: %#v, %v", loaded, err)
	}
}

func TestStorePreservesCallerDeadlineDuringTaskLockContention(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := store.Update(context.Background(), "task-1", func(task Task) (Task, error) {
			close(entered)
			<-release
			return task, nil
		})
		finished <- err
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("first update ended early: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("first update did not reach its callback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := store.Update(ctx, "task-1", func(task Task) (Task, error) {
		return task, nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("second update error = %v, want caller deadline", err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestStoreReportsPublishedRecordWhenSyncFails(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("injected sync failure")
	store.syncDirectory = func(*os.Root) error { return syncFailure }
	updated, err := store.Update(context.Background(), "task-1", func(current Task) (Task, error) {
		snapshot := current.Snapshot()
		snapshot.Definition.Body = "published"
		return NewMigrated(snapshot)
	})
	if !errors.Is(err, ErrPublicationUnconfirmed) || !errors.Is(err, syncFailure) ||
		updated.Definition().Body != "published" {
		t.Fatalf("Update = %#v, %v; want published but unconfirmed", updated, err)
	}
	loaded, err := store.Read(context.Background(), "task-1")
	if err != nil || loaded.Definition().Body != "published" {
		t.Fatalf("published record cannot be reconciled: %#v, %v", loaded, err)
	}
}

func TestStoreReportsPublicationWhenRecordsDirectoryMoves(t *testing.T) {
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); err != nil {
		t.Fatal(err)
	}
	store.syncDirectory = func(records *os.Root) error {
		if err := os.Rename(filepath.Join(root, "records"), filepath.Join(root, "records-moved")); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(root, "records"), 0o700); err != nil {
			return err
		}
		return syncDirectory(records)
	}
	updated, err := store.Update(context.Background(), "task-1", func(current Task) (Task, error) {
		snapshot := current.Snapshot()
		snapshot.Definition.Body = "published"
		return NewMigrated(snapshot)
	})
	if !errors.Is(err, ErrPublicationUnconfirmed) || updated.Definition().Body != "published" {
		t.Fatalf("Update = %#v, %v; want published record to reconcile", updated, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "records-moved", "task-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	published, err := Decode(data)
	if err != nil || published.Definition().Body != "published" {
		t.Fatalf("moved record = %#v, %v; want published", published, err)
	}
}

func TestStoreRejectsTaskIDTooLongForLockName(t *testing.T) {
	longID := strings.Repeat("x", 245)
	if _, err := NewQueued(Definition{ID: longID, Title: "Work", Repo: "exmachina", Kind: "ship"}); !errors.Is(
		err,
		ErrInvalidTask,
	) {
		t.Fatalf("NewQueued accepted oversized task ID: %v", err)
	}
	root := versionTwoRoot(t)
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), longID); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Read accepted oversized task ID: %v", err)
	}
}
