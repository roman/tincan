package taskstate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/roman/tincan/pkg/firstmate"
)

// LockedTask permits durable transitions during one synchronous operation.
// Use it only inside its WithTask callback, without concurrent goroutines.
type LockedTask struct {
	current Task
	active  bool
	failure error
	publish func(Task) error
	check   func() error
}

// Current returns the most recently published task.
func (task *LockedTask) Current() Task { return task.current }

// CheckCurrent verifies the pinned binding without publishing a task record.
func (task *LockedTask) CheckCurrent() error {
	if task == nil || !task.active || task.check == nil {
		return ErrInvalidTask
	}
	if task.failure != nil {
		return task.failure
	}
	return task.check()
}

// Apply publishes a transition before returning, including unchanged records.
// New worker identities require Store.ReserveAdoption's global reservation lock.
func (task *LockedTask) Apply(transition func(Task) (Task, error)) (Task, error) {
	return task.apply(func(current Task) (Task, error) {
		if transition == nil {
			return Task{}, ErrInvalidTask
		}
		candidate, err := transition(current)
		if err != nil {
			return Task{}, err
		}
		if !sameWorkerIdentity(current, candidate) {
			return Task{}, ErrWorkerConflict
		}
		return candidate, nil
	})
}

func (task *LockedTask) apply(transition func(Task) (Task, error)) (Task, error) {
	if task == nil || !task.active || transition == nil {
		return Task{}, ErrInvalidTask
	}
	if task.failure != nil {
		return task.current, task.failure
	}
	candidate, err := transition(task.current)
	if err != nil {
		return Task{}, err
	}
	if candidate.ID() != task.current.ID() {
		return Task{}, ErrInvalidTask
	}
	err = task.publish(candidate)
	if err == nil || errors.Is(err, ErrPublicationUnconfirmed) {
		task.current = candidate
	}
	if err != nil {
		task.failure = err
	}
	return task.current, err
}

// WithTask holds the version guard and task lock for the entire callback.
// Each Apply persists immediately. A callback error retains earlier writes.
// Do not call a locking Store method from the callback.
func (store *Store) WithTask(
	ctx context.Context, taskID string, operation func(*LockedTask) error,
) (Task, error) {
	if operation == nil {
		return Task{}, ErrInvalidTask
	}
	id, err := parseStoreTaskID(taskID)
	if err != nil {
		return Task{}, err
	}
	var current Task
	err = store.withVersionTwo(ctx, func(root *os.Root) error {
		var err error
		current, err = store.withTaskPinned(ctx, root, id, operation)
		return err
	})
	return current, err
}

func (store *Store) withTaskPinned(
	ctx context.Context, root *os.Root, id firstmate.TaskID, operation func(*LockedTask) error,
) (Task, error) {
	var current Task
	err := withTaskLock(ctx, root, id, func() error {
		records, selectedDirectory, err := openRecords(root)
		if err != nil {
			return err
		}
		defer records.Close()
		var selectedRecord os.FileInfo
		current, selectedRecord, err = readRecord(records, id.String())
		if err != nil {
			return err
		}
		selectedRoot, err := root.Stat(".")
		if err != nil {
			return err
		}
		locked := &LockedTask{current: current, active: true}
		locked.check = func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			currentRoot, err := os.Lstat(store.path)
			if err != nil || !currentRoot.IsDir() || !os.SameFile(selectedRoot, currentRoot) {
				return fmt.Errorf("%w: task-state root changed", ErrInvalidTask)
			}
			currentDirectory, err := root.Lstat("records")
			if err != nil || !os.SameFile(selectedDirectory, currentDirectory) {
				return fmt.Errorf("%w: task records directory changed", ErrInvalidTask)
			}
			selectedTask, selected, err := readRecord(records, id.String())
			if err != nil || !os.SameFile(selectedRecord, selected) {
				return fmt.Errorf("%w: task record changed", ErrInvalidTask)
			}
			currentData, err := locked.current.Encode()
			if err != nil {
				return err
			}
			selectedData, err := selectedTask.Encode()
			if err != nil || !bytes.Equal(currentData, selectedData) {
				return fmt.Errorf("%w: task record contents changed", ErrInvalidTask)
			}
			return nil
		}
		defer func() { locked.active = false }()
		locked.publish = func(candidate Task) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := candidate.Encode()
			if err != nil {
				return err
			}
			if err := writeRecord(root, records, selectedDirectory, id.String(), data,
				selectedRecord, store.syncDirectory); err != nil {
				return err
			}
			var refreshed Task
			refreshed, selectedRecord, err = readRecord(records, id.String())
			if err != nil {
				return errors.Join(ErrPublicationUnconfirmed, err)
			}
			refreshedData, err := refreshed.Encode()
			if err != nil || !bytes.Equal(data, refreshedData) {
				return errors.Join(ErrPublicationUnconfirmed, ErrInvalidTask, err)
			}
			return nil
		}
		err = operation(locked)
		current = locked.current
		if locked.failure != nil && !errors.Is(err, locked.failure) {
			return errors.Join(err, locked.failure)
		}
		return err
	})
	return current, err
}
