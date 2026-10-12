package taskstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestInitializeFreshLayouts(t *testing.T) {
	for _, layout := range []string{"absent", "empty", "primary and profile", "interrupted records", "interrupted marker", "long interrupted marker"} {
		t.Run(layout, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "state")
			if layout != "absent" {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			switch layout {
			case "primary and profile":
				for _, name := range []string{"primary.json", "primary.claim.lock"} {
					if err := os.WriteFile(filepath.Join(root, name), []byte("preserve"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink("missing-profile", filepath.Join(root, "profile")); err != nil {
					t.Fatal(err)
				}
			case "interrupted records":
				if err := os.Mkdir(filepath.Join(root, "records"), 0700); err != nil {
					t.Fatal(err)
				}
			case "long interrupted marker":
				if err := os.WriteFile(filepath.Join(root, ".task-state-version-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), []byte("2"), 0600); err != nil {
					t.Fatal(err)
				}
			case "interrupted marker":
				if err := os.WriteFile(filepath.Join(root, ".task-state-version-AAAAAAAAAAAAAAAAAAAAAAAAAA"), []byte("2"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := Initialize(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			store, err := NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := store.List(context.Background())
			if err != nil || len(tasks) != 0 {
				t.Fatalf("tasks %v error %v", tasks, err)
			}
			if layout == "primary and profile" {
				for _, name := range []string{"primary.json", "primary.claim.lock"} {
					data, err := os.ReadFile(filepath.Join(root, name))
					if err != nil || string(data) != "preserve" {
						t.Fatalf("changed %s: %q %v", name, data, err)
					}
				}
				target, err := os.Readlink(filepath.Join(root, "profile"))
				if err != nil || target != "missing-profile" {
					t.Fatalf("changed profile: %q %v", target, err)
				}
			}
		})
	}
}

func TestInitializePreservesExistingTasks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := Initialize(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	task, err := NewQueued(Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, "records/task-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(root, "records/task-1.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("changed task: %q %v", after, err)
	}
}

func TestInitializeRejectsUnsafeFreshLayouts(t *testing.T) {
	for _, layout := range []string{"backlog.md", "tasks", "unknown", "records with task", "records with owner", "records symlink", "primary symlink", "profile directory", "marker temporary symlink", "unknown marker temporary", "pending", "explicit legacy"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			file := func(name, contents string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			directory := func(name string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			link := func(name string) {
				t.Helper()
				if err := os.Symlink(t.TempDir(), filepath.Join(root, name)); err != nil {
					t.Fatal(err)
				}
			}
			switch layout {
			case "backlog.md", "unknown":
				file(layout, "preserve")
			case "tasks":
				directory("tasks")
			case "records with task":
				directory("records")
				file("records/task-1.json", "preserve")
			case "records with owner":
				directory("records")
				file("records/.migration-owner", "tincan-taskstate-v2\n")
			case "records symlink":
				link("records")
			case "primary symlink":
				link("primary.json")
			case "profile directory":
				directory("profile")
			case "marker temporary symlink":
				link(".task-state-version-AAAAAAAAAAAAAAAAAAAAAAAAAA")
			case "unknown marker temporary":
				file(".task-state-version-unknown", "preserve")
			case "pending":
				file(".task-state-migration-pending", "2\n")
			case "explicit legacy":
				file(".task-state-version", "1\n")
			}
			before, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := Initialize(context.Background(), root); err == nil {
				t.Fatal("accepted unsafe layout")
			}
			after, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) {
				t.Fatalf("rejection changed root entries: %v -> %v", before, after)
			}
			for i, entry := range before {
				if entry.Name() != after[i].Name() || entry.Type() != after[i].Type() {
					t.Fatal("rejection changed root entries")
				}
			}
		})
	}
}

func TestInitializeDoesNotRepairMissingVersionTwoRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := Initialize(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "records")); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), root); err == nil {
		t.Fatal("accepted missing version two records")
	}
	if _, err := os.Stat(filepath.Join(root, "records")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recreated records: %v", err)
	}
}

func TestInitializeRetriesPreparationSyncFailure(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		root := filepath.Join(t.TempDir(), "state")
		calls := 0
		failure := errors.New("sync failed")
		err := initialize(context.Background(), root, func(*os.Root) error {
			calls++
			if calls == failAt {
				return failure
			}
			return nil
		})
		if !errors.Is(err, failure) {
			t.Fatalf("sync %d error %v", failAt, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".task-state-version")); !errors.Is(
			err,
			os.ErrNotExist,
		) {
			t.Fatalf("published before preparation synced: %v", err)
		}
		if err := Initialize(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentInitialization(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	var group sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); results <- Initialize(context.Background(), root) }()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
}
