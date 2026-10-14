// Package stateversion prevents task-state commands from crossing a storage migration.
package stateversion

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/roman/tincan/internal/lockfile"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrIncompatible = errors.New("task-state version is incompatible with this command")
var ErrLockTimeout = errors.New("task-state version lock timed out")
var ErrPublicationUnconfirmed = errors.New("task-state version publication requires reconciliation")
var ErrCleanupUnconfirmed = errors.New("task-state migration cleanup requires reconciliation")

const versionFile = ".task-state-version"
const pendingFile = ".task-state-migration-pending"
const lockSuffix = ".task-state-version.lock"
const lockTimeout = 2 * time.Second

type guardPurpose int

const (
	commandPurpose guardPurpose = iota
	migrationPurpose
	recoveryPurpose
	initializationPurpose
)

// Guard holds a task-state lease with a specific publication purpose.
type Guard struct {
	root          *os.Root
	parent        *os.Root
	lock          *os.File
	purpose       guardPurpose
	syncDirectory func(*os.Root) error
}

// AcquireLegacy holds a shared lease while a version 1 command uses task state.
func AcquireLegacy(ctx context.Context, root string) (*Guard, error) {
	return acquireVersion(ctx, root, syscall.LOCK_SH, 1)
}

// AcquireVersionTwo holds a shared lease while a version 2 command uses task state.
func AcquireVersionTwo(ctx context.Context, root string) (*Guard, error) {
	return acquireVersion(ctx, root, syscall.LOCK_SH, 2)
}

// AcquireRecovery holds an exclusive lease while a pending migration is repaired.
func AcquireRecovery(ctx context.Context, root string) (*Guard, error) {
	guard, err := acquire(ctx, root, syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	pending, err := guard.readPending()
	if err != nil || !pending {
		guard.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrIncompatible
	}
	if _, err := guard.readVersion(); err != nil {
		guard.Close()
		return nil, err
	}
	guard.purpose = recoveryPurpose
	return guard, nil
}

// AcquireMigration holds an exclusive lease for a version switch.
func AcquireMigration(ctx context.Context, root string) (*Guard, error) {
	guard, err := acquireVersion(ctx, root, syscall.LOCK_EX, 1)
	if err != nil {
		return nil, err
	}
	if guard.root == nil {
		guard.Close()
		return nil, fmt.Errorf("task-state root does not exist: %s", root)
	}
	guard.purpose = migrationPurpose
	return guard, nil
}

func acquireVersion(ctx context.Context, path string, mode, expected int) (*Guard, error) {
	guard, err := acquire(ctx, path, mode)
	if err != nil {
		return nil, err
	}
	version, err := guard.readVersion()
	if err != nil || version != expected {
		guard.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf(
			"%w: found version %d; requires version %d",
			ErrIncompatible,
			version,
			expected,
		)
	}
	pending, err := guard.readPending()
	if err != nil || pending {
		guard.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: task-state migration requires recovery", ErrIncompatible)
	}
	return guard, nil
}

// StateRoot returns the pinned directory while the guard remains open.
// Callers must not close the returned handle.
func (guard *Guard) StateRoot() *os.Root {
	if guard == nil {
		return nil
	}
	return guard.root
}

func acquire(ctx context.Context, path string, mode int) (*Guard, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("task-state root must be a clean absolute path")
	}
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	baseName := filepath.Base(path)
	lockName := "." + strings.TrimPrefix(baseName, ".") + lockSuffix
	lock, err := lockfile.Open(parent, lockName)
	if err != nil {
		parent.Close()
		return nil, err
	}
	guard := &Guard{
		parent: parent, lock: lock,
		syncDirectory: syncStateDirectory,
	}
	selected, err := parent.Lstat(lockName)
	if err != nil {
		guard.Close()
		return nil, err
	}
	opened, err := lock.Stat()
	if err != nil || !selected.Mode().IsRegular() || !os.SameFile(selected, opened) {
		guard.Close()
		return nil, fmt.Errorf("invalid task-state version lock")
	}
	timer := time.NewTimer(lockTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			guard.Close()
			return nil, err
		}
		err = syscall.Flock(int(lock.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			guard.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			guard.Close()
			return nil, ctx.Err()
		case <-timer.C:
			guard.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, ErrLockTimeout
		case <-ticker.C:
		}
	}
	if err := ctx.Err(); err != nil {
		guard.Close()
		return nil, err
	}
	selectedRoot, err := parent.Lstat(baseName)
	if errors.Is(err, os.ErrNotExist) {
		return guard, nil
	}
	if err != nil {
		guard.Close()
		return nil, err
	}
	if !selectedRoot.IsDir() {
		guard.Close()
		return nil, fmt.Errorf("task-state root must be a directory: %s", path)
	}
	root, err := parent.OpenRoot(baseName)
	if err != nil {
		guard.Close()
		return nil, err
	}
	openedRoot, err := root.Stat(".")
	if err != nil || !os.SameFile(selectedRoot, openedRoot) {
		root.Close()
		guard.Close()
		return nil, fmt.Errorf("task-state root changed during lock acquisition: %s", path)
	}
	guard.root = root
	return guard, nil
}

func (guard *Guard) readVersion() (int, error) {
	if guard.root == nil {
		return 1, nil
	}
	selected, err := guard.root.Lstat(versionFile)
	if errors.Is(err, os.ErrNotExist) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if !selected.Mode().IsRegular() {
		return 0, ErrIncompatible
	}
	file, err := guard.root.Open(versionFile)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(selected, opened) {
		return 0, ErrIncompatible
	}
	data, err := io.ReadAll(io.LimitReader(file, 3))
	if err != nil {
		return 0, err
	}
	switch string(data) {
	case "1\n":
		return 1, nil
	case "2\n":
		return 2, nil
	default:
		return 0, ErrIncompatible
	}
}

func (guard *Guard) readPending() (bool, error) {
	if guard.root == nil {
		return false, nil
	}
	selected, err := guard.root.Lstat(pendingFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !selected.Mode().IsRegular() {
		return false, ErrIncompatible
	}
	file, err := guard.root.Open(pendingFile)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(selected, opened) {
		return false, ErrIncompatible
	}
	data, err := io.ReadAll(io.LimitReader(file, 3))
	if err != nil {
		return false, err
	}
	if string(data) != "2\n" {
		return false, ErrIncompatible
	}
	return true, nil
}

// PublishVersion commits a new marker while the migration lease is held.
func (guard *Guard) PublishVersion(version int) error {
	if guard == nil || (guard.purpose != migrationPurpose && guard.purpose != recoveryPurpose) ||
		version != 2 {
		return ErrIncompatible
	}
	current, err := guard.readVersion()
	if err != nil {
		return err
	}
	if current != 1 && current != version {
		return ErrIncompatible
	}
	if err := guard.preparePending(); err != nil {
		return err
	}
	if current == version {
		return guard.ConfirmPublishedVersion()
	}
	if err := guard.publishVersionMarker(version); err != nil {
		return err
	}
	return guard.finishPending()
}

func (guard *Guard) publishVersionMarker(version int) error {
	name := ".task-state-version-" + rand.Text()
	temporary, err := guard.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer guard.root.Remove(name)
	if _, err := fmt.Fprintf(temporary, "%d\n", version); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := guard.root.Rename(name, versionFile); err != nil {
		return err
	}
	if err := guard.syncDirectory(guard.root); err != nil {
		return errors.Join(ErrPublicationUnconfirmed, err)
	}
	return nil
}

// ConfirmPublishedVersion syncs an already visible version 2 marker.
func (guard *Guard) ConfirmPublishedVersion() error {
	if guard == nil || (guard.purpose != migrationPurpose && guard.purpose != recoveryPurpose) ||
		guard.root == nil {
		return ErrIncompatible
	}
	version, err := guard.readVersion()
	if err != nil {
		return err
	}
	if version != 2 {
		return ErrIncompatible
	}
	pending, err := guard.readPending()
	if err != nil {
		return err
	}
	if !pending {
		return ErrIncompatible
	}
	if err := guard.syncDirectory(guard.root); err != nil {
		return errors.Join(ErrPublicationUnconfirmed, err)
	}
	return guard.finishPending()
}

func (guard *Guard) preparePending() error {
	pending, err := guard.readPending()
	if err != nil {
		return err
	}
	if !pending {
		name := ".task-state-migration-" + rand.Text()
		file, err := guard.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer guard.root.Remove(name)
		if _, err := file.WriteString("2\n"); err != nil {
			file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := guard.root.Link(name, pendingFile); err != nil {
			return err
		}
	}
	if err := guard.syncDirectory(guard.root); err != nil {
		return errors.Join(ErrPublicationUnconfirmed, err)
	}
	return nil
}

func (guard *Guard) finishPending() error {
	if err := guard.root.Remove(pendingFile); err != nil {
		return errors.Join(ErrCleanupUnconfirmed, err)
	}
	if err := guard.syncDirectory(guard.root); err != nil {
		return errors.Join(ErrCleanupUnconfirmed, err)
	}
	return nil
}

func syncStateDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// Close releases the lease and its directory handle.
func (guard *Guard) Close() error {
	if guard == nil {
		return nil
	}
	var rootError error
	if guard.root != nil {
		rootError = guard.root.Close()
	}
	return errors.Join(guard.lock.Close(), guard.parent.Close(), rootError)
}
