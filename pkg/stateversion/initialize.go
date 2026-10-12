package stateversion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// InitializationState distinguishes fresh roots from completed initialization.
type InitializationState int

const (
	FreshRoot InitializationState = iota
	ExistingVersionTwo
)

// AcquireInitialization pins a root under an exclusive task-state lease.
// The caller must validate fresh root contents before publishing its version.
func AcquireInitialization(ctx context.Context, path string) (*Guard, InitializationState, error) {
	guard, err := acquire(ctx, path, syscall.LOCK_EX)
	if err != nil {
		return nil, FreshRoot, err
	}
	guard.purpose = initializationPurpose
	state, err := guard.initializationState()
	if err != nil {
		guard.Close()
		return nil, FreshRoot, err
	}
	if guard.root != nil {
		return guard, state, nil
	}
	name := filepath.Base(path)
	if err := guard.parent.Mkdir(name, 0700); err != nil {
		guard.Close()
		return nil, FreshRoot, err
	}
	selected, err := guard.parent.Lstat(name)
	if err != nil {
		guard.Close()
		return nil, FreshRoot, err
	}
	if !selected.IsDir() {
		guard.Close()
		return nil, FreshRoot, fmt.Errorf("initialization root must be a real directory")
	}
	root, err := guard.parent.OpenRoot(name)
	if err != nil {
		guard.Close()
		return nil, FreshRoot, err
	}
	guard.root = root
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(selected, opened) {
		guard.Close()
		return nil, FreshRoot, fmt.Errorf("initialization root changed during acquisition")
	}
	return guard, FreshRoot, nil
}

func (guard *Guard) initializationState() (InitializationState, error) {
	pending, err := guard.readPending()
	if err != nil {
		return FreshRoot, err
	}
	if pending {
		return FreshRoot, fmt.Errorf("%w: task-state migration requires recovery", ErrIncompatible)
	}
	if guard.root == nil {
		return FreshRoot, nil
	}
	_, err = guard.root.Lstat(versionFile)
	if errors.Is(err, os.ErrNotExist) {
		return FreshRoot, nil
	}
	if err != nil {
		return FreshRoot, err
	}
	version, err := guard.readVersion()
	if err != nil {
		return FreshRoot, err
	}
	if version != 2 {
		return FreshRoot, ErrIncompatible
	}
	return ExistingVersionTwo, nil
}

// PublishInitialVersion publishes fresh state or confirms an existing version two root.
// The caller must prepare and sync records before publishing fresh state.
func (guard *Guard) PublishInitialVersion() error {
	if guard == nil || guard.purpose != initializationPurpose || guard.root == nil {
		return ErrIncompatible
	}
	state, err := guard.initializationState()
	if err != nil {
		return err
	}
	if state == FreshRoot {
		if err := guard.publishVersionMarker(2); err != nil {
			return err
		}
	} else if err := guard.syncDirectory(guard.root); err != nil {
		return errors.Join(ErrPublicationUnconfirmed, err)
	}
	if err := guard.syncDirectory(guard.parent); err != nil {
		return errors.Join(ErrPublicationUnconfirmed, err)
	}
	return nil
}
