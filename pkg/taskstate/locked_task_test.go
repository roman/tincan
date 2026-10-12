package taskstate

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func storeForLockedTask(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(versionTwoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), queuedStoreTask(t)); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestWithTaskKeepsEarlierWritesAfterCallbackFailure(t *testing.T) {
	store := storeForLockedTask(t)
	failure := errors.New("external operation failed")
	_, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
		if _, err := task.Apply(func(current Task) (Task, error) {
			return current.PlaceHold(Hold{Reason: "Waiting"})
		}); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("callback error = %v", err)
	}
	stored, err := store.Read(context.Background(), "task-1")
	if err != nil || stored.Definition().Hold == nil {
		t.Fatalf("saved intermediate task = %#v, %v", stored.Snapshot(), err)
	}
	if _, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
		_, err := task.Apply(Task.ReleaseHold)
		return err
	}); err != nil {
		t.Fatalf("retry after callback error = %v", err)
	}
}

func TestWithTaskRepublishesUnchangedTransitions(t *testing.T) {
	store := storeForLockedTask(t)
	publications := 0
	store.syncDirectory = func(root *os.Root) error {
		publications++
		return syncDirectory(root)
	}
	_, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
		for range 2 {
			if _, err := task.Apply(Task.ReleaseHold); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || publications != 2 {
		t.Fatalf("publications = %d, error = %v", publications, err)
	}
}

func TestWithTaskStopsAfterUncertainPublication(t *testing.T) {
	store := storeForLockedTask(t)
	store.syncDirectory = func(*os.Root) error { return errors.New("sync failed") }
	_, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
		if _, err := task.Apply(Task.ReleaseHold); !errors.Is(err, ErrPublicationUnconfirmed) {
			t.Fatalf("first publication = %v", err)
		}
		_, err := task.Apply(func(current Task) (Task, error) {
			t.Fatal("transition ran after uncertain publication")
			return current, nil
		})
		return err
	})
	if !errors.Is(err, ErrPublicationUnconfirmed) {
		t.Fatalf("second publication = %v", err)
	}
}

func TestWithTaskRetainsCandidateAfterPublishedRecordDisappears(t *testing.T) {
	store := storeForLockedTask(t)
	store.syncDirectory = func(records *os.Root) error {
		store.syncDirectory = syncDirectory
		if err := syncDirectory(records); err != nil {
			return err
		}
		return records.Remove("task-1.json")
	}
	updated, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
		_, err := task.Apply(func(current Task) (Task, error) {
			return current.PlaceHold(Hold{Reason: "Waiting"})
		})
		if !errors.Is(err, ErrPublicationUnconfirmed) {
			t.Errorf("refresh failure = %v", err)
		}
		if task.Current().Definition().Hold == nil {
			t.Error("published candidate was discarded")
		}
		// A swallowed publication error must still stop the operation.
		return nil
	})
	if !errors.Is(err, ErrPublicationUnconfirmed) || updated.Definition().Hold == nil {
		t.Fatalf("published result = %#v, %v", updated.Snapshot(), err)
	}
}

func TestWithTaskDoesNotOverwriteReplacementAfterPublication(t *testing.T) {
	store := storeForLockedTask(t)
	replacement, err := queuedStoreTask(t).PlaceHold(Hold{Reason: "External change"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := replacement.Encode()
	if err != nil {
		t.Fatal(err)
	}
	store.syncDirectory = func(records *os.Root) error {
		store.syncDirectory = syncDirectory
		if err := syncDirectory(records); err != nil {
			return err
		}
		file, err := records.OpenFile("replacement", os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(data)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return err
		}
		return records.Rename("replacement", "task-1.json")
	}
	_, err = store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
		if _, err := task.Apply(Task.ReleaseHold); !errors.Is(err, ErrPublicationUnconfirmed) {
			t.Errorf("replacement error = %v", err)
		}
		_, err := task.Apply(func(current Task) (Task, error) {
			t.Error("transition ran after record replacement")
			return current, nil
		})
		return err
	})
	if !errors.Is(err, ErrPublicationUnconfirmed) {
		t.Fatalf("operation error = %v", err)
	}
	stored, err := store.Read(context.Background(), "task-1")
	if err != nil || stored.Definition().Hold == nil ||
		stored.Definition().Hold.Reason != "External change" {
		t.Fatalf("replacement was overwritten: %#v, %v", stored.Snapshot(), err)
	}
}

func TestWithTaskReleasesLockAndClosesHandle(t *testing.T) {
	store := storeForLockedTask(t)
	var retained *LockedTask
	_, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
		retained = task
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		if _, err := store.Update(ctx, "task-1", Task.ReleaseHold); !errors.Is(
			err, context.DeadlineExceeded,
		) {
			t.Fatalf("competing update = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retained.Apply(Task.ReleaseHold); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("closed handle = %v", err)
	}
	if _, err := store.Update(context.Background(), "task-1", Task.ReleaseHold); err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
}

func TestWithTaskPreservesIdentityRestrictions(t *testing.T) {
	for _, test := range []struct {
		name       string
		transition func(Task) (Task, error)
		want       error
	}{
		{name: "task identity", transition: func(current Task) (Task, error) {
			snapshot := current.Snapshot()
			snapshot.Definition.ID = "task-2"
			return NewMigrated(snapshot)
		}, want: ErrInvalidTask},
		{name: "worker identity", transition: func(current Task) (Task, error) {
			return current.ReserveAdoption(workerForTransition())
		}, want: ErrWorkerConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := storeForLockedTask(t)
			_, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
				_, err := task.Apply(test.transition)
				return err
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("identity error = %v", err)
			}
		})
	}
}
