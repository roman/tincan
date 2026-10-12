package taskstate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/stateversion"
)

var ErrBackupPublicationUnconfirmed = errors.New(
	"legacy backup publication requires reconciliation",
)

// BackupFile records the bytes or link target and permissions of one source entry.
type BackupFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

// BackupManifest identifies a complete version 1 task-state snapshot.
type BackupManifest struct {
	Format           int          `json:"format"`
	Source           string       `json:"source"`
	TaskStateVersion int          `json:"task_state_version"`
	Files            []BackupFile `json:"files"`
}

// BackupLegacy copies the guarded version 1 state into a new sibling directory.
// A crash before publication can leave a .backup-* staging directory beside
// the source. Operators may remove that directory after confirming no backup
// process still uses it.
func BackupLegacy(ctx context.Context, sourcePath, destinationPath string) (BackupManifest, error) {
	source, err := firstmate.ParseAbsolutePath(sourcePath)
	if err != nil {
		return BackupManifest{}, err
	}
	destination, err := firstmate.ParseAbsolutePath(destinationPath)
	if err != nil {
		return BackupManifest{}, err
	}
	guard, err := stateversion.AcquireMigration(ctx, source.String())
	if err != nil {
		return BackupManifest{}, err
	}
	defer guard.Close()
	return publishLegacyBackup(ctx, guard.StateRoot(), source.String(), destination.String(),
		backupPublication{syncParent: syncDirectory, rename: renameBackupNoReplace})
}

type backupPublication struct {
	syncParent func(*os.Root) error
	rename     func(*os.Root, string, string) error
}

func publishLegacyBackup(
	ctx context.Context, source *os.Root, sourcePath, destinationPath string,
	publication backupPublication,
) (result BackupManifest, resultErr error) {
	parentPath := filepath.Dir(destinationPath)
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return BackupManifest{}, err
	}
	defer parent.Close()
	sourceParent, err := os.OpenRoot(filepath.Dir(sourcePath))
	if err != nil {
		return BackupManifest{}, err
	}
	defer sourceParent.Close()
	selectedParent, sourceParentErr := sourceParent.Stat(".")
	openedParent, destinationParentErr := parent.Stat(".")
	if err := errors.Join(sourceParentErr, destinationParentErr); err != nil {
		return BackupManifest{}, err
	}
	if !os.SameFile(selectedParent, openedParent) {
		return BackupManifest{}, fmt.Errorf(
			"backup destination must be a sibling of the task-state root",
		)
	}
	selectedSource, err := sourceParent.Lstat(filepath.Base(sourcePath))
	if err != nil {
		return BackupManifest{}, err
	}
	openedSource, err := source.Stat(".")
	if err != nil || !os.SameFile(selectedSource, openedSource) {
		return BackupManifest{}, fmt.Errorf("task-state root changed before backup")
	}
	baseName := filepath.Base(destinationPath)
	if baseName == filepath.Base(sourcePath) {
		return BackupManifest{}, fmt.Errorf("backup destination must differ from task-state root")
	}
	tempName := ".backup-" + rand.Text()
	if err := parent.Mkdir(tempName, 0o700); err != nil {
		return BackupManifest{}, err
	}
	published := false
	defer func() {
		if !published {
			resultErr = errors.Join(resultErr, removeBackupStage(parent, tempName))
		}
	}()
	backup, err := parent.OpenRoot(tempName)
	if err != nil {
		return BackupManifest{}, err
	}
	defer backup.Close()
	manifest := BackupManifest{
		Format:           2,
		Source:           sourcePath,
		TaskStateVersion: 1,
		Files:            []BackupFile{},
	}
	if err := copyLegacyDirectory(ctx, source, backup, "", &manifest); err != nil {
		return BackupManifest{}, err
	}
	if err := ctx.Err(); err != nil {
		return BackupManifest{}, err
	}
	if err := confirmSourceEntry(sourceParent, filepath.Base(sourcePath), selectedSource); err != nil {
		return BackupManifest{}, err
	}
	if err := writeBackupManifest(backup, manifest); err != nil {
		return BackupManifest{}, err
	}
	if err := syncDirectory(backup); err != nil {
		return BackupManifest{}, err
	}
	if err := ctx.Err(); err != nil {
		return BackupManifest{}, err
	}
	stagedInfo, err := backup.Stat(".")
	if err != nil {
		return BackupManifest{}, err
	}
	if err := publication.rename(parent, tempName, baseName); err != nil {
		return BackupManifest{}, err
	}
	published = true
	if err := publication.syncParent(parent); err != nil {
		return manifest, errors.Join(ErrBackupPublicationUnconfirmed, err)
	}
	finalInfo, err := parent.Lstat(baseName)
	if err != nil || !os.SameFile(stagedInfo, finalInfo) {
		return manifest, errors.Join(ErrBackupPublicationUnconfirmed, err)
	}
	return manifest, nil
}

func removeBackupStage(parent *os.Root, name string) error {
	stage, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	accessErr := allowBackupStageRemoval(stage)
	closeErr := stage.Close()
	if err := errors.Join(accessErr, closeErr); err != nil {
		return err
	}
	return parent.RemoveAll(name)
}

func allowBackupStageRemoval(directory *os.Root) error {
	handle, err := directory.Open(".")
	if err != nil {
		return err
	}
	if err := handle.Chmod(0o700); err != nil {
		handle.Close()
		return err
	}
	entries, readErr := handle.ReadDir(-1)
	closeErr := handle.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child, err := directory.OpenRoot(entry.Name())
		if err != nil {
			return err
		}
		accessErr := allowBackupStageRemoval(child)
		closeErr := child.Close()
		if err := errors.Join(accessErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func copyLegacyDirectory(
	ctx context.Context, source, destination *os.Root, prefix string, manifest *BackupManifest,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	listing, err := source.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := listing.ReadDir(-1)
	closeErr := listing.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	sort.Slice(
		entries,
		func(left, right int) bool { return entries[left].Name() < entries[right].Name() },
	)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if prefix == "" && name == "manifest.json" {
			return fmt.Errorf("legacy state contains reserved backup manifest name")
		}
		selected, err := source.Lstat(name)
		if err != nil {
			return err
		}
		path := filepath.Join(prefix, name)
		switch {
		case selected.IsDir():
			if err := copyLegacySubdirectory(ctx, source, destination, name, path, selected, manifest); err != nil {
				return err
			}
		case selected.Mode().IsRegular():
			file, err := copyLegacyFile(ctx, source, destination, name, path, selected)
			if err != nil {
				return err
			}
			manifest.Files = append(manifest.Files, file)
		case selected.Mode()&os.ModeSymlink != 0:
			link, err := copyLegacySymlink(source, destination, name, path, selected)
			if err != nil {
				return err
			}
			manifest.Files = append(manifest.Files, link)
		default:
			return fmt.Errorf("unsupported legacy state entry: %s", path)
		}
		if err := confirmSourceEntry(source, name, selected); err != nil {
			return err
		}
	}
	return syncDirectory(destination)
}

func confirmSourceEntry(root *os.Root, name string, selected os.FileInfo) error {
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(selected, current) || selected.Mode() != current.Mode() ||
		selected.Size() != current.Size() || !selected.ModTime().Equal(current.ModTime()) {
		return fmt.Errorf("legacy state entry changed during backup: %s", name)
	}
	return nil
}

func copyLegacySubdirectory(
	ctx context.Context, source, destination *os.Root, name, path string,
	selected os.FileInfo, manifest *BackupManifest,
) error {
	childSource, err := source.OpenRoot(name)
	if err != nil {
		return err
	}
	defer childSource.Close()
	opened, err := childSource.Stat(".")
	if err != nil || !os.SameFile(selected, opened) {
		return fmt.Errorf("legacy directory changed during backup: %s", path)
	}
	if err := destination.Mkdir(name, 0o700); err != nil {
		return err
	}
	childDestination, err := destination.OpenRoot(name)
	if err != nil {
		return err
	}
	defer childDestination.Close()
	if err := copyLegacyDirectory(ctx, childSource, childDestination, path, manifest); err != nil {
		return err
	}
	directory, err := childDestination.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Chmod(selected.Mode().Perm()); err != nil {
		return err
	}
	return directory.Sync()
}

func copyLegacyFile(
	ctx context.Context, source, destination *os.Root, name, path string, selected os.FileInfo,
) (BackupFile, error) {
	input, err := source.Open(name)
	if err != nil {
		return BackupFile{}, err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(selected, opened) {
		return BackupFile{}, fmt.Errorf("legacy file changed during backup: %s", path)
	}
	output, err := destination.OpenFile(
		name,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		selected.Mode().Perm(),
	)
	if err != nil {
		return BackupFile{}, err
	}
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			output.Close()
			return BackupFile{}, err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			written, writeErr := io.MultiWriter(output, hash).Write(buffer[:count])
			size += int64(written)
			if writeErr != nil || written != count {
				output.Close()
				return BackupFile{}, errors.Join(writeErr, io.ErrShortWrite)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			output.Close()
			return BackupFile{}, readErr
		}
	}
	current, err := input.Stat()
	if err != nil || !os.SameFile(opened, current) || size != selected.Size() ||
		!current.ModTime().Equal(selected.ModTime()) {
		output.Close()
		return BackupFile{}, fmt.Errorf("legacy file changed during backup: %s", path)
	}
	if err := output.Chmod(selected.Mode().Perm()); err != nil {
		output.Close()
		return BackupFile{}, err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return BackupFile{}, err
	}
	if err := output.Close(); err != nil {
		return BackupFile{}, err
	}
	return BackupFile{
		Path: filepath.ToSlash(path), Kind: "file", Size: size,
		SHA256: hex.EncodeToString(hash.Sum(nil)), Mode: uint32(selected.Mode().Perm()),
	}, nil
}

func copyLegacySymlink(
	source, destination *os.Root, name, path string, selected os.FileInfo,
) (BackupFile, error) {
	target, err := source.Readlink(name)
	if err != nil {
		return BackupFile{}, err
	}
	if !utf8.ValidString(target) {
		return BackupFile{}, fmt.Errorf("legacy link target is not UTF-8: %s", path)
	}
	if err := destination.Symlink(target, name); err != nil {
		return BackupFile{}, err
	}
	created, err := destination.Lstat(name)
	if err != nil || created.Mode()&os.ModeSymlink == 0 {
		return BackupFile{}, fmt.Errorf("backup link was not created: %s", path)
	}
	digest := sha256.Sum256([]byte(target))
	return BackupFile{
		Path: filepath.ToSlash(path), Kind: "symlink", Target: target,
		Size: int64(len(target)), SHA256: hex.EncodeToString(digest[:]),
		Mode: uint32(selected.Mode().Perm()),
	}, nil
}

func writeBackupManifest(root *os.Root, manifest BackupManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	file, err := root.OpenFile("manifest.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
