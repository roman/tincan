package taskstate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/stateversion"
)

const migrationOwnerName = ".migration-owner"
const migrationOwnerContent = "tincan-taskstate-v2\n"

// MigrateLegacy backs up and converts a quiescent version 1 task-state root.
// Earlier task writers do not hold the version gate and must be stopped first.
func MigrateLegacy(ctx context.Context, rootPath, backupPath string) error {
	if _, err := firstmate.ParseAbsolutePath(backupPath); err != nil {
		return err
	}
	guard, err := stateversion.AcquireMigration(ctx, rootPath)
	if err != nil {
		return err
	}
	defer guard.Close()
	root := guard.StateRoot()
	tasks, err := inventoryLegacy(root)
	if err != nil {
		return err
	}
	if _, err := migrationRecordsExist(root); err != nil {
		return err
	}
	if _, err := os.Lstat(backupPath); errors.Is(err, os.ErrNotExist) {
		if _, err := publishLegacyBackup(ctx, root, rootPath, backupPath,
			backupPublication{syncParent: syncDirectory, rename: renameBackupNoReplace}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := verifyMigrationBackup(ctx, root, rootPath, backupPath); err != nil {
		return err
	}
	if err := publishMigrationRecords(ctx, root, tasks); err != nil {
		return err
	}
	if err := verifyMigrationRecords(root, tasks); err != nil {
		return err
	}
	return guard.PublishVersion(2)
}

// RecoverLegacyMigration verifies the backup and records before finishing a pending switch.
func RecoverLegacyMigration(ctx context.Context, rootPath, backupPath string) error {
	if _, err := firstmate.ParseAbsolutePath(backupPath); err != nil {
		return err
	}
	guard, err := stateversion.AcquireRecovery(ctx, rootPath)
	if err != nil {
		return err
	}
	defer guard.Close()
	root := guard.StateRoot()
	tasks, err := inventoryLegacy(root)
	if err != nil {
		return err
	}
	if err := verifyMigrationBackup(ctx, root, rootPath, backupPath); err != nil {
		return err
	}
	if err := verifyMigrationRecords(root, tasks); err != nil {
		return err
	}
	return guard.PublishVersion(2)
}

func verifyMigrationBackup(ctx context.Context, root *os.Root, rootPath, backupPath string) error {
	if filepath.Dir(backupPath) != filepath.Dir(rootPath) {
		return fmt.Errorf("migration backup must be a sibling directory")
	}
	parent, err := os.OpenRoot(filepath.Dir(backupPath))
	if err != nil {
		return err
	}
	defer parent.Close()
	selected, err := parent.Lstat(filepath.Base(backupPath))
	if err != nil {
		return err
	}
	if !selected.IsDir() {
		return fmt.Errorf("migration backup must be a sibling directory")
	}
	backup, err := parent.OpenRoot(filepath.Base(backupPath))
	if err != nil {
		return err
	}
	defer backup.Close()
	opened, err := backup.Stat(".")
	if err != nil || !os.SameFile(selected, opened) {
		return fmt.Errorf("migration backup changed during open")
	}
	data, err := readLegacyFile(backup, "manifest.json", 16<<20)
	if err != nil {
		return err
	}
	var manifest BackupManifest
	if err := decodeLegacyJSON(data, &manifest); err != nil || manifest.Format != 2 ||
		manifest.Source != rootPath || manifest.TaskStateVersion != 1 {
		return fmt.Errorf("migration backup manifest does not match the legacy root")
	}
	expected := make(map[string]BackupFile, len(manifest.Files))
	for _, entry := range manifest.Files {
		path := filepath.FromSlash(entry.Path)
		if !filepath.IsLocal(path) || filepath.Clean(path) != path ||
			filepath.ToSlash(path) != entry.Path || entry.Path == "manifest.json" {
			return fmt.Errorf("invalid migration backup path %q", entry.Path)
		}
		if _, exists := expected[entry.Path]; exists {
			return fmt.Errorf("duplicate migration backup path %q", entry.Path)
		}
		expected[entry.Path] = entry
	}
	if entry, found := expected["backlog.md"]; !found || entry.Kind != "file" {
		return fmt.Errorf("migration backup lacks the backlog")
	}
	directories := make(map[string]os.FileMode)
	if err := verifyBackupDirectory(ctx, backup, "", expected, directories); err != nil {
		return err
	}
	if len(expected) != 0 {
		return fmt.Errorf("migration backup lacks %d manifest entries", len(expected))
	}
	liveFiles := make(map[string]struct{}, len(manifest.Files))
	for _, entry := range manifest.Files {
		if isMigrationOwnedPath(entry.Path) {
			continue
		}
		liveFiles[entry.Path] = struct{}{}
		selected, err := root.Lstat(filepath.FromSlash(entry.Path))
		if err != nil {
			return fmt.Errorf("legacy source entry %q: %w", entry.Path, err)
		}
		if err := verifyBackupEntry(ctx, root, filepath.FromSlash(entry.Path), selected, entry); err != nil {
			return fmt.Errorf("legacy source entry %q differs from backup: %w", entry.Path, err)
		}
	}
	if err := verifyLiveDirectory(ctx, root, "", liveFiles, directories); err != nil {
		return err
	}
	if len(liveFiles) != 0 {
		return fmt.Errorf("legacy source lacks %d backup entries", len(liveFiles))
	}
	if len(directories) != 0 {
		return fmt.Errorf("legacy source lacks %d backup directories", len(directories))
	}
	// A prior publication can report an uncertain result after rename succeeds.
	// Sync the parent before the version marker can make this backup mandatory.
	if err := syncDirectory(parent); err != nil {
		return err
	}
	confirmed, err := parent.Lstat(filepath.Base(backupPath))
	if err != nil || !os.SameFile(selected, confirmed) {
		return fmt.Errorf("migration backup changed after parent sync")
	}
	return nil
}

func isMigrationOwnedPath(path string) bool {
	return path == "records" || strings.HasPrefix(path, "records/") ||
		path == ".task-state-version" || path == ".task-state-migration-pending" ||
		strings.HasPrefix(path, ".records-migration-") ||
		strings.HasPrefix(path, ".task-state-migration-") ||
		strings.HasPrefix(path, ".task-state-version-")
}

func verifyLiveDirectory(
	ctx context.Context,
	root *os.Root,
	prefix string,
	expectedFiles map[string]struct{},
	expectedDirectories map[string]os.FileMode,
) error {
	listing, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := listing.ReadDir(-1)
	closeErr := listing.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		path := filepath.ToSlash(filepath.Join(prefix, name))
		if isMigrationOwnedPath(path) {
			continue
		}
		selected, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if selected.IsDir() {
			mode, found := expectedDirectories[path]
			if !found || selected.Mode().Perm() != mode {
				return fmt.Errorf("legacy source directory differs from backup: %s", path)
			}
			delete(expectedDirectories, path)
			child, err := root.OpenRoot(name)
			if err != nil {
				return err
			}
			verifyErr := verifyLiveDirectory(ctx, child, path, expectedFiles, expectedDirectories)
			closeErr := child.Close()
			if err := errors.Join(verifyErr, closeErr); err != nil {
				return err
			}
			continue
		}
		if _, found := expectedFiles[path]; !found {
			return fmt.Errorf("legacy source contains entry absent from backup: %s", path)
		}
		delete(expectedFiles, path)
	}
	return nil
}

func verifyBackupDirectory(
	ctx context.Context, backup *os.Root, prefix string,
	expected map[string]BackupFile, directories map[string]os.FileMode,
) error {
	listing, err := backup.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := listing.ReadDir(-1)
	closeErr := listing.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if prefix == "" && name == "manifest.json" {
			continue
		}
		path := filepath.ToSlash(filepath.Join(prefix, name))
		selected, err := backup.Lstat(name)
		if err != nil {
			return err
		}
		if selected.IsDir() {
			if !isMigrationOwnedPath(path) {
				directories[path] = selected.Mode().Perm()
			}
			child, err := backup.OpenRoot(name)
			if err != nil {
				return err
			}
			verifyErr := verifyBackupDirectory(ctx, child, path, expected, directories)
			closeErr := child.Close()
			if err := errors.Join(verifyErr, closeErr); err != nil {
				return err
			}
			continue
		}
		want, found := expected[path]
		if !found {
			return fmt.Errorf("migration backup contains unexpected entry %q", path)
		}
		if err := verifyBackupEntry(ctx, backup, name, selected, want); err != nil {
			return fmt.Errorf("migration backup entry %q: %w", path, err)
		}
		delete(expected, path)
	}
	return nil
}

func verifyBackupEntry(
	ctx context.Context, root *os.Root, name string, selected os.FileInfo, want BackupFile,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if uint32(selected.Mode().Perm()) != want.Mode {
		return fmt.Errorf("mode differs from manifest")
	}
	var size int64
	hash := sha256.New()
	switch want.Kind {
	case "file":
		if !selected.Mode().IsRegular() {
			return fmt.Errorf("file kind differs from manifest")
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(selected, opened) {
			file.Close()
			return fmt.Errorf("file changed during verification")
		}
		buffer := make([]byte, 32*1024)
		for {
			if err := ctx.Err(); err != nil {
				file.Close()
				return err
			}
			count, readErr := file.Read(buffer)
			if count > 0 {
				size += int64(count)
				_, _ = hash.Write(buffer[:count])
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				file.Close()
				return readErr
			}
		}
		closeErr := file.Close()
		if closeErr != nil {
			return closeErr
		}
	case "symlink":
		if selected.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("link kind differs from manifest")
		}
		target, err := root.Readlink(name)
		if err != nil {
			return err
		}
		if target != want.Target {
			return fmt.Errorf("link target differs from manifest")
		}
		size = int64(len(target))
		_, _ = hash.Write([]byte(target))
	default:
		return fmt.Errorf("invalid manifest entry kind %q", want.Kind)
	}
	if size != want.Size || hex.EncodeToString(hash.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("content differs from manifest")
	}
	return nil
}

func publishMigrationRecords(ctx context.Context, root *os.Root, tasks []Task) error {
	exists, err := migrationRecordsExist(root)
	if err != nil {
		return err
	}
	if exists {
		if verifyMigrationRecords(root, tasks) == nil {
			return ctx.Err()
		}
		if err := root.RemoveAll("records"); err != nil {
			return err
		}
		if err := syncDirectory(root); err != nil {
			return err
		}
	}
	name := ".records-migration-" + rand.Text()
	if err := root.Mkdir(name, 0o700); err != nil {
		return err
	}
	defer root.RemoveAll(name)
	stage, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer stage.Close()
	if err := writeMigrationFile(stage, migrationOwnerName, []byte(migrationOwnerContent)); err != nil {
		return err
	}
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := task.Encode()
		if err != nil {
			return err
		}
		if err := writeMigrationFile(stage, task.ID()+".json", data); err != nil {
			return err
		}
	}
	if err := syncDirectory(stage); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Rename(name, "records"); err != nil {
		return err
	}
	return syncDirectory(root)
}

func migrationRecordsExist(root *os.Root) (bool, error) {
	if _, err := root.Lstat("records"); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	records, _, err := openRecords(root)
	if err != nil {
		return false, err
	}
	defer records.Close()
	data, err := readLegacyFile(records, migrationOwnerName, 64)
	if err != nil || string(data) != migrationOwnerContent {
		return false, fmt.Errorf("version 2 records lack the migration owner marker")
	}
	return true, nil
}

func writeMigrationFile(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	count, writeErr := file.Write(data)
	if writeErr == nil && count != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		file.Close()
		return writeErr
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func verifyMigrationRecords(root *os.Root, tasks []Task) error {
	records, _, err := openRecords(root)
	if err != nil {
		return err
	}
	defer records.Close()
	data, err := readLegacyFile(records, migrationOwnerName, 64)
	if err != nil || string(data) != migrationOwnerContent {
		return fmt.Errorf("version 2 records lack the migration owner marker")
	}
	directory, err := records.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if len(entries) != len(tasks)+1 {
		return fmt.Errorf("version 2 record count differs from legacy inventory")
	}
	expected := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		expected[task.ID()] = task
	}
	for _, entry := range entries {
		if entry.Name() == migrationOwnerName {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("unexpected version 2 record %q", entry.Name())
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		want, found := expected[id]
		if !found {
			return fmt.Errorf("unexpected version 2 task %q", id)
		}
		got, _, err := readRecord(records, id)
		if err != nil {
			return err
		}
		wantData, err := want.Encode()
		if err != nil {
			return err
		}
		gotData, err := got.Encode()
		if err != nil || !bytes.Equal(gotData, wantData) {
			return fmt.Errorf("version 2 task %q differs from legacy inventory", id)
		}
	}
	return nil
}
