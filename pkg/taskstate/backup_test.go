package taskstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/stateversion"
)

func TestBackupLegacyCopiesTaskStateAndManifest(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "state")
	if err := os.MkdirAll(filepath.Join(source, "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "crew"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"backlog.md":                "# Backlog\n",
		"tasks/task-1.meta":         "endpoint_task_id=task-1\n",
		"tasks/task-1.receipt.json": `{"task_id":"task-1"}`,
		"crew/task-1.json":          `{"task_id":"task-1"}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(parent, "backup")
	manifest, err := BackupLegacy(context.Background(), source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Format != 2 || manifest.Source != source || manifest.TaskStateVersion != 1 ||
		len(manifest.Files) != len(files) {
		t.Fatalf("manifest = %#v", manifest)
	}
	if info, err := os.Stat(filepath.Join(destination, "tasks")); err != nil || !info.IsDir() {
		t.Fatalf("backup tasks directory = %v, %v", info, err)
	}
	for name, content := range files {
		copied, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(copied) != content {
			t.Fatalf("backup %s = %q, %v", name, copied, err)
		}
		digest := sha256.Sum256([]byte(content))
		entry := BackupFile{
			Path:   filepath.ToSlash(name),
			Kind:   "file",
			Size:   int64(len(content)),
			SHA256: hex.EncodeToString(digest[:]),
			Mode:   0o600,
		}
		if !slices.Contains(manifest.Files, entry) {
			t.Fatalf("manifest lacks %#v: %#v", entry, manifest.Files)
		}
	}
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored BackupManifest
	if err := json.Unmarshal(data, &stored); err != nil || stored.Source != source ||
		!slices.Equal(stored.Files, manifest.Files) {
		t.Fatalf("stored manifest = %#v, %v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(source, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup changed source: %v", err)
	}
}

func TestBackupLegacyPreservesSymlinkWithoutFollowingIt(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "state")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "backlog.md"), []byte("# Backlog\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside"), []byte("do not copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(source, "profile")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "backup")
	manifest, err := BackupLegacy(context.Background(), source, destination)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(destination, "profile"))
	if err != nil || target != "../outside" {
		t.Fatalf("backed up link = %q, %v", target, err)
	}
	sourceLink, err := os.Lstat(filepath.Join(source, "profile"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("../outside"))
	entry := BackupFile{
		Path: "profile", Kind: "symlink", Target: "../outside", Size: int64(len("../outside")),
		SHA256: hex.EncodeToString(digest[:]), Mode: uint32(sourceLink.Mode().Perm()),
	}
	if !slices.Contains(manifest.Files, entry) {
		t.Fatalf("manifest lacks link entry %#v: %#v", entry, manifest.Files)
	}
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored BackupManifest
	if err := json.Unmarshal(data, &stored); err != nil || !slices.Contains(stored.Files, entry) {
		t.Fatalf("stored manifest lacks link entry %#v: %#v, %v", entry, stored.Files, err)
	}
}

func TestBackupLegacyRejectsInvalidUTF8LinkTarget(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "state")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(string([]byte{0xff}), filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "backup")
	if _, err := BackupLegacy(context.Background(), source, destination); err == nil {
		t.Fatal("backup accepted a link target that the manifest cannot preserve")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid backup was published: %v", err)
	}
}

func TestBackupLegacyRejectsDestinationInsideSource(t *testing.T) {
	source := t.TempDir()
	if _, err := BackupLegacy(context.Background(), source, filepath.Join(source, "backup")); err == nil {
		t.Fatal("backup accepted destination inside source")
	}
}

func TestBackupLegacyRefusesVersionTwo(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "state")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".task-state-version"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupLegacy(context.Background(), source, filepath.Join(parent, "backup")); !errors.Is(
		err,
		stateversion.ErrIncompatible,
	) {
		t.Fatalf("BackupLegacy error = %v, want incompatible version", err)
	}
}

func TestBackupLegacyPreservesReadOnlyDirectory(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "state")
	tasks := filepath.Join(source, "tasks")
	if err := os.MkdirAll(tasks, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasks, "task-1.meta"), []byte("record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tasks, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(tasks, 0o700) })
	destination := filepath.Join(parent, "backup")
	t.Cleanup(func() { os.Chmod(filepath.Join(destination, "tasks"), 0o700) })
	if _, err := BackupLegacy(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destination, "tasks"))
	if err != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("backup tasks mode = %v, %v; want 0500", info, err)
	}
}

func TestBackupLegacyKeepsExistingDestination(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "state")
	destination := filepath.Join(parent, "backup")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupLegacy(context.Background(), source, destination); !errors.Is(
		err,
		os.ErrExist,
	) {
		t.Fatalf("BackupLegacy error = %v, want existing destination", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("existing destination was removed: %v", err)
	}
}

func TestBackupDestinationRejectsAliasIntoSource(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "state")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "state-alias")
	if err := os.Symlink("state", alias); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupLegacy(context.Background(), source, filepath.Join(alias, "backup")); err == nil {
		t.Fatal("backup destination alias inside source was accepted")
	}
}

func TestBackupRemainsPrivateUntilComplete(t *testing.T) {
	parent := t.TempDir()
	sourcePath := filepath.Join(parent, "state")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination := filepath.Join(parent, "backup")
	_, err = publishLegacyBackup(context.Background(), source, sourcePath, destination,
		backupPublication{
			syncParent: syncDirectory,
			rename: func(root *os.Root, oldName, newName string) error {
				if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("destination visible before publication: %v", err)
				}
				return renameBackupNoReplace(root, oldName, newName)
			},
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackupKeepsDestinationCreatedDuringCopy(t *testing.T) {
	parent := t.TempDir()
	sourcePath := filepath.Join(parent, "state")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination := filepath.Join(parent, "backup")
	nested := filepath.Join(sourcePath, "nested")
	if err := os.Mkdir(nested, 0o500); err != nil {
		t.Fatal(err)
	}
	_, err = publishLegacyBackup(context.Background(), source, sourcePath, destination,
		backupPublication{
			syncParent: syncDirectory,
			rename: func(root *os.Root, oldName, newName string) error {
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				return renameBackupNoReplace(root, oldName, newName)
			},
		})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("backup error = %v, want destination exists", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("concurrent destination removed: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".backup-") {
			t.Fatalf("failed backup left staging directory: %s", entry.Name())
		}
	}
}

func TestBackupDetectsDestinationReplacementAfterRename(t *testing.T) {
	parent := t.TempDir()
	sourcePath := filepath.Join(parent, "state")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination := filepath.Join(parent, "backup")
	_, err = publishLegacyBackup(context.Background(), source, sourcePath, destination,
		backupPublication{
			rename: renameBackupNoReplace,
			syncParent: func(root *os.Root) error {
				if err := os.Rename(destination, filepath.Join(parent, "moved-backup")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				return syncDirectory(root)
			},
		})
	if !errors.Is(err, ErrBackupPublicationUnconfirmed) {
		t.Fatalf("backup error = %v, want unconfirmed publication", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("replacement destination removed: %v", err)
	}
}

func TestBackupLegacyPreservesFilePermissions(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "state")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, "backlog.md")
	if err := os.WriteFile(file, []byte("# Backlog\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o666); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "backup")
	if _, err := BackupLegacy(context.Background(), source, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destination, "backlog.md"))
	if err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("backup file mode = %v, %v; want 0666", info, err)
	}
}

func TestBackupReportsPublishedSnapshotWhenParentSyncFails(t *testing.T) {
	parent := t.TempDir()
	sourcePath := filepath.Join(parent, "state")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePath, "backlog.md"), []byte("# Backlog\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	syncFailure := errors.New("injected parent sync failure")
	destination := filepath.Join(parent, "backup")
	manifest, err := publishLegacyBackup(
		context.Background(), source, sourcePath, destination,
		backupPublication{
			rename: renameBackupNoReplace,
			syncParent: func(root *os.Root) error {
				return syncFailure
			},
		},
	)
	if !errors.Is(err, ErrBackupPublicationUnconfirmed) || !errors.Is(err, syncFailure) ||
		len(manifest.Files) != 1 {
		t.Fatalf("backup = %#v, %v; want published snapshot", manifest, err)
	}
	if _, err := os.Stat(filepath.Join(destination, "manifest.json")); err != nil {
		t.Fatalf("published backup is missing manifest: %v", err)
	}
}

func TestBackupDetectsReplacedSourceEntry(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.OpenFile("record", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	selected, err := root.Lstat("record")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("record", "old-record"); err != nil {
		t.Fatal(err)
	}
	file, err = root.OpenFile("record", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err := confirmSourceEntry(root, "record", selected); err == nil {
		t.Fatal("source entry replacement was accepted")
	}
}

func TestBackupLegacyStopsBeforeManifestAfterCancellation(t *testing.T) {
	parent := t.TempDir()
	sourcePath := filepath.Join(parent, "state")
	if err := os.Mkdir(sourcePath, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := os.OpenRoot(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(parent, "backup")
	_, err = publishLegacyBackup(ctx, source, sourcePath, destination,
		backupPublication{syncParent: syncDirectory, rename: renameBackupNoReplace})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("backup error = %v, want cancellation", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled backup left destination: %v", err)
	}
}
