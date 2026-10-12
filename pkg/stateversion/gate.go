// Package stateversion prevents task-state commands from crossing a storage migration.
package stateversion

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrIncompatible = errors.New("task-state version is incompatible with this command")
var ErrLockTimeout = errors.New("task-state version lock timed out")

const versionFile = ".task-state-version"
const lockSuffix = ".task-state-version.lock"
const lockTimeout = 2 * time.Second

// Guard holds a shared command lease or an exclusive migration lease.
type Guard struct {
	root      *os.Root
	parent    *os.Root
	lock      *os.File
	exclusive bool
}

// AcquireLegacy holds a shared lease while a version 1 command uses task state.
func AcquireLegacy(ctx context.Context, root string) (*Guard, error) {
	return acquireVersionOne(ctx, root, syscall.LOCK_SH)
}

// AcquireMigration holds an exclusive lease for a version switch.
func AcquireMigration(ctx context.Context, root string) (*Guard, error) {
	guard, err := acquireVersionOne(ctx, root, syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	if guard.root == nil {
		guard.Close()
		return nil, fmt.Errorf("task-state root does not exist: %s", root)
	}
	return guard, nil
}

func acquireVersionOne(ctx context.Context, path string, mode int) (*Guard, error) {
	guard, err := acquire(ctx, path, mode)
	if err != nil {
		return nil, err
	}
	version, err := guard.readVersion()
	if err != nil || version != 1 {
		guard.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: found version %d; requires version 1", ErrIncompatible, version)
	}
	return guard, nil
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
	lock, err := parent.OpenFile(lockName, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		parent.Close()
		return nil, err
	}
	guard := &Guard{parent: parent, lock: lock, exclusive: mode == syscall.LOCK_EX}
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
	deadline, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	for {
		err = syscall.Flock(int(lock.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			guard.Close()
			return nil, err
		}
		select {
		case <-deadline.Done():
			guard.Close()
			if errors.Is(deadline.Err(), context.DeadlineExceeded) {
				return nil, ErrLockTimeout
			}
			return nil, deadline.Err()
		case <-time.After(10 * time.Millisecond):
		}
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

// PublishVersion commits a new marker while the migration lease is held.
func (guard *Guard) PublishVersion(version int) error {
	if guard == nil || !guard.exclusive || version != 2 {
		return ErrIncompatible
	}
	current, err := guard.readVersion()
	if err != nil {
		return err
	}
	if current != 1 && current != version {
		return ErrIncompatible
	}
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
	directory, err := guard.root.Open(".")
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
