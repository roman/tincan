package taskstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/stateversion"
)

func TestMigrateLegacyPublishesVerifiedRecords(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-queued - Waiting (repo: exmachina) (kind: ship)\n## Done\n- [x] task-done - Finished (repo: exmachina) (kind: ship)\n",
	)
	if err := os.Symlink("/nix/store/example", filepath.Join(root, "profile")); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(parent, "backup")
	if err := MigrateLegacy(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	if _, err := stateversion.AcquireLegacy(context.Background(), root); !errors.Is(
		err,
		stateversion.ErrIncompatible,
	) {
		t.Fatalf("legacy guard error = %v, want incompatible", err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range map[string]State{"task-queued": Queued, "task-done": Done} {
		task, err := store.Read(context.Background(), id)
		if err != nil || task.State() != state {
			t.Fatalf("task %q = %#v, %v", id, task.Snapshot(), err)
		}
	}
	if _, err := os.Lstat(filepath.Join(backupPath, "profile")); err != nil {
		t.Fatalf("backup lacks installer link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "backlog.md")); err != nil {
		t.Fatalf("legacy backlog was removed: %v", err)
	}
}

func TestMigrateLegacyLeavesVersionOneAfterPreflightFailure(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## In flight\n- **task-1** - Running (repo: exmachina) (kind: ship)\n",
	)
	backupPath := filepath.Join(parent, "backup")
	if err := MigrateLegacy(context.Background(), root, backupPath); err == nil {
		t.Fatal("migration accepted a task without its worker")
	}
	guard, err := stateversion.AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatalf("version 1 guard was blocked: %v", err)
	}
	guard.Close()
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup published after failed preflight: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "records")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("records published after failed preflight: %v", err)
	}
}

func TestMigrateLegacyReplacesStaleRecordsFromEarlierAttempt(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.MkdirAll(filepath.Join(root, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-1 - Current (repo: exmachina) (kind: ship)\n",
	)
	writeLegacyFile(t, root, "records/.migration-owner", "tincan-taskstate-v2\n")
	stale, err := NewQueued(
		Definition{ID: "task-1", Title: "Stale", Repo: "exmachina", Kind: "ship"},
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := stale.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(t, root, "records/task-1.json", string(data))
	if err := MigrateLegacy(context.Background(), root, filepath.Join(parent, "backup")); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Read(context.Background(), "task-1")
	if err != nil || current.Definition().Title != "Current" {
		t.Fatalf("migrated task title = %q, %v", current.Definition().Title, err)
	}
}

func TestMigrateLegacyRejectsUnownedRecordsBeforeBackup(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.MkdirAll(filepath.Join(root, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-1 - Work (repo: exmachina) (kind: ship)\n",
	)
	writeLegacyFile(t, root, "records/user.json", "keep")
	backupPath := filepath.Join(parent, "backup")
	if err := MigrateLegacy(context.Background(), root, backupPath); err == nil {
		t.Fatal("migration accepted an unowned records directory")
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup published before preflight: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "records/user.json"))
	if err != nil || string(data) != "keep" {
		t.Fatalf("unowned records changed: %q, %v", data, err)
	}
}

func TestMigrateLegacyResumesPublishedBackup(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-1 - Work (repo: exmachina) (kind: ship)\n",
	)
	backupPath := filepath.Join(parent, "backup")
	if _, err := BackupLegacy(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacy(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyRejectsChangedLiveLink(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-1 - Work (repo: exmachina) (kind: ship)\n",
	)
	link := filepath.Join(root, "profile")
	if err := os.Symlink("/nix/store/old", link); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(parent, "backup")
	if _, err := BackupLegacy(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nix/store/new", link); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacy(context.Background(), root, backupPath); err == nil {
		t.Fatal("migration accepted an older backup after installer link changed")
	}
	if _, err := os.Stat(filepath.Join(root, ".task-state-version")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("failed migration published version 2: %v", err)
	}
}

func TestMigrateLegacyRejectsNewLiveFileAfterBackup(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-1 - Work (repo: exmachina) (kind: ship)\n",
	)
	backupPath := filepath.Join(parent, "backup")
	if _, err := BackupLegacy(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nix/store/new", filepath.Join(root, "profile")); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacy(context.Background(), root, backupPath); err == nil {
		t.Fatal("migration accepted a backup without the new installer link")
	}
}

func TestMigrateLegacyRejectsChangedBackupDirectory(t *testing.T) {
	for _, change := range []string{"remove", "mode"} {
		t.Run(change, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "state")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			writeLegacyFile(
				t,
				root,
				"backlog.md",
				"# Tasks\n## Queued\n- [ ] task-1 - Work (repo: exmachina) (kind: ship)\n",
			)
			if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
				t.Fatal(err)
			}
			backupPath := filepath.Join(parent, "backup")
			if _, err := BackupLegacy(context.Background(), root, backupPath); err != nil {
				t.Fatal(err)
			}
			backupDirectory := filepath.Join(backupPath, "empty")
			var err error
			if change == "remove" {
				err = os.Remove(backupDirectory)
			} else {
				err = os.Chmod(backupDirectory, 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := MigrateLegacy(context.Background(), root, backupPath); err == nil {
				t.Fatal("migration accepted a backup with a changed directory")
			}
		})
	}
}

func TestRecoverLegacyMigrationConfirmsPendingVersion(t *testing.T) {
	for _, version := range []string{"", "2\n"} {
		t.Run("version-"+strings.TrimSpace(version), func(t *testing.T) {
			testRecoverLegacyMigrationConfirmsPendingVersion(t, version)
		})
	}
}

func testRecoverLegacyMigrationConfirmsPendingVersion(t *testing.T, version string) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.MkdirAll(filepath.Join(root, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-1 - Work (repo: exmachina) (kind: ship)\n",
	)
	backupPath := filepath.Join(parent, "backup")
	if _, err := BackupLegacy(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	task, err := NewQueued(Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := task.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(t, root, "records/.migration-owner", "tincan-taskstate-v2\n")
	writeLegacyFile(t, root, "records/task-1.json", string(data))
	writeLegacyFile(t, root, ".task-state-migration-pending", "2\n")
	if version != "" {
		writeLegacyFile(t, root, ".task-state-version", version)
	}
	if err := RecoverLegacyMigration(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverLegacyMigrationKeepsPendingOnMismatch(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.MkdirAll(filepath.Join(root, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-1 - Current (repo: exmachina) (kind: ship)\n",
	)
	backupPath := filepath.Join(parent, "backup")
	if _, err := BackupLegacy(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	task, err := NewQueued(
		Definition{ID: "task-1", Title: "Stale", Repo: "exmachina", Kind: "ship"},
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := task.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(t, root, "records/.migration-owner", "tincan-taskstate-v2\n")
	writeLegacyFile(t, root, "records/task-1.json", string(data))
	writeLegacyFile(t, root, ".task-state-migration-pending", "2\n")
	if err := RecoverLegacyMigration(context.Background(), root, backupPath); err == nil {
		t.Fatal("recovery accepted stale task records")
	}
	if _, err := os.Stat(filepath.Join(root, ".task-state-version")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("failed recovery published version 2: %v", err)
	}
	pending, err := os.ReadFile(filepath.Join(root, ".task-state-migration-pending"))
	if err != nil || string(pending) != "2\n" {
		t.Fatalf("failed recovery cleared pending marker: %q, %v", pending, err)
	}
	if _, err := stateversion.AcquireLegacy(context.Background(), root); !errors.Is(
		err,
		stateversion.ErrIncompatible,
	) {
		t.Fatalf("pending migration stopped guarding version 1: %v", err)
	}
}

func TestRecoverLegacyMigrationRejectsDamagedBackup(t *testing.T) {
	tests := []struct {
		name   string
		damage func(*testing.T, string)
	}{
		{name: "missing metadata", damage: func(t *testing.T, backup string) {
			if err := os.Remove(filepath.Join(backup, "tasks/task-1.meta")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed link", damage: func(t *testing.T, backup string) {
			if err := os.Remove(filepath.Join(backup, "profile")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/nix/store/other", filepath.Join(backup, "profile")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "state")
			if err := os.MkdirAll(filepath.Join(root, "tasks"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "crew"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeLegacyFile(
				t,
				root,
				"backlog.md",
				"# Tasks\n## In flight\n- **task-1** - Work (repo: exmachina) (kind: ship)\n",
			)
			writeLegacyJSON(t, root, "crew/task-1.json", legacyCrewRecord("task-1", "gen-1"))
			writeLegacyFile(t, root, "tasks/task-1.meta", legacyTaskMetadata("task-1", "gen-1"))
			if err := os.Symlink("/nix/store/example", filepath.Join(root, "profile")); err != nil {
				t.Fatal(err)
			}
			backupPath := filepath.Join(parent, "backup")
			if _, err := BackupLegacy(context.Background(), root, backupPath); err != nil {
				t.Fatal(err)
			}
			selected, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			tasks, err := inventoryLegacy(selected)
			selected.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "records"), 0o700); err != nil {
				t.Fatal(err)
			}
			writeLegacyFile(t, root, "records/.migration-owner", "tincan-taskstate-v2\n")
			data, err := tasks[0].Encode()
			if err != nil {
				t.Fatal(err)
			}
			writeLegacyFile(t, root, "records/task-1.json", string(data))
			writeLegacyFile(t, root, ".task-state-migration-pending", "2\n")
			test.damage(t, backupPath)
			if err := RecoverLegacyMigration(context.Background(), root, backupPath); err == nil {
				t.Fatal("recovery accepted damaged backup")
			}
		})
	}
}

func TestRecoverLegacyMigrationRejectsChangedLiveMetadata(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.MkdirAll(filepath.Join(root, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "crew"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## In flight\n- **task-1** - Work (repo: exmachina) (kind: ship)\n",
	)
	writeLegacyJSON(t, root, "crew/task-1.json", legacyCrewRecord("task-1", "gen-1"))
	metadata := legacyTaskMetadata("task-1", "gen-1")
	writeLegacyFile(t, root, "tasks/task-1.meta", metadata)
	backupPath := filepath.Join(parent, "backup")
	if _, err := BackupLegacy(context.Background(), root, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "records"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(t, root, "records/.migration-owner", migrationOwnerContent)
	writeLegacyFile(t, root, ".task-state-migration-pending", "2\n")
	writeLegacyFile(t, root, "tasks/task-1.meta", strings.Replace(metadata,
		"herdr_workspace_id=workspace-1", "herdr_workspace_id=workspace-2", 1))
	selected, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := inventoryLegacy(selected)
	selected.Close()
	if err != nil {
		t.Fatal(err)
	}
	data, err := tasks[0].Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(t, root, "records/task-1.json", string(data))
	selected, err = os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMigrationRecords(selected, tasks); err != nil {
		t.Fatalf("test setup has mismatched version 2 records: %v", err)
	}
	selected.Close()
	if err := RecoverLegacyMigration(context.Background(), root, backupPath); err == nil {
		t.Fatal("recovery accepted an older backup after metadata changed")
	}
	if _, err := os.Stat(filepath.Join(root, ".task-state-version")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("failed recovery published version 2: %v", err)
	}
}
