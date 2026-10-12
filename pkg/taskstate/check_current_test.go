package taskstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckCurrentIsReadOnlyAndTracksSuccessfulPublication(t *testing.T) {
	store := storeForLockedTask(t)
	publications := 0
	store.syncDirectory = func(root *os.Root) error { publications++; return syncDirectory(root) }
	var saved *LockedTask
	_, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
		saved = task
		for range 2 {
			if err := task.CheckCurrent(); err != nil {
				return err
			}
		}
		if publications != 0 {
			t.Fatalf("read-only checks published %d records", publications)
		}
		if _, err := task.Apply(func(current Task) (Task, error) { return current.PlaceHold(Hold{Reason: "Waiting"}) }); err != nil {
			return err
		}
		return task.CheckCurrent()
	})
	if err != nil || publications != 1 {
		t.Fatalf("CheckCurrent = %v, publications = %d", err, publications)
	}
	if err := saved.CheckCurrent(); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("closed check = %v", err)
	}
	var missing *LockedTask
	if err := missing.CheckCurrent(); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("nil check = %v", err)
	}
	if err := new(LockedTask).CheckCurrent(); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("zero-value check = %v", err)
	}
}

func TestCheckCurrentRejectsChangedBindingStorage(t *testing.T) {
	for _, change := range []string{"record replaced", "record removed", "record edited in place", "records replaced", "state root replaced", "state root symlink"} {
		t.Run(change, func(t *testing.T) {
			store := storeForLockedTask(t)
			_, err := store.WithTask(context.Background(), "task-1", func(task *LockedTask) error {
				record := filepath.Join(store.path, "records", "task-1.json")
				switch change {
				case "record replaced":
					data, err := os.ReadFile(record)
					if err != nil {
						t.Fatal(err)
					}
					replacement := filepath.Join(store.path, "replacement.json")
					if err := os.WriteFile(replacement, data, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, record); err != nil {
						t.Fatal(err)
					}
				case "record removed":
					if err := os.Remove(record); err != nil {
						t.Fatal(err)
					}
				case "record edited in place":
					before, err := os.Stat(record)
					if err != nil {
						t.Fatal(err)
					}
					changed, err := task.Current().PlaceHold(Hold{Reason: "External edit"})
					if err != nil {
						t.Fatal(err)
					}
					data, err := changed.Encode()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(record, data, 0o600); err != nil {
						t.Fatal(err)
					}
					after, err := os.Stat(record)
					if err != nil || !os.SameFile(before, after) {
						t.Fatalf("fixture replaced inode: %v", err)
					}
				case "records replaced":
					path := filepath.Join(store.path, "records")
					if err := os.Rename(path, filepath.Join(store.path, "old-records")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				case "state root replaced", "state root symlink":
					old := store.path + "-old"
					if err := os.Rename(store.path, old); err != nil {
						t.Fatal(err)
					}
					if change == "state root symlink" {
						if err := os.Symlink(old, store.path); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Mkdir(store.path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				return task.CheckCurrent()
			})
			if !errors.Is(err, ErrInvalidTask) {
				t.Fatalf("changed storage check = %v, want invalid binding", err)
			}
		})
	}
}

func TestCheckCurrentRejectsCancellationAndPublicationFailure(t *testing.T) {
	for _, reason := range []string{"cancelled", "uncertain publication"} {
		t.Run(reason, func(t *testing.T) {
			store := storeForLockedTask(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("injected sync failure")
			want := error(context.Canceled)
			_, err := store.WithTask(ctx, "task-1", func(task *LockedTask) error {
				if reason == "cancelled" {
					cancel()
				} else {
					store.syncDirectory = func(*os.Root) error { return failure }
					_, applyErr := task.Apply(Task.ReleaseHold)
					if !errors.Is(applyErr, ErrPublicationUnconfirmed) {
						t.Fatalf("Apply = %v", applyErr)
					}
					want = ErrPublicationUnconfirmed
				}
				checkErr := task.CheckCurrent()
				if !errors.Is(checkErr, want) {
					t.Fatalf("CheckCurrent = %v, want %v", checkErr, want)
				}
				return checkErr
			})
			if !errors.Is(err, want) {
				t.Fatalf("WithTask = %v, want %v", err, want)
			}
		})
	}
}
