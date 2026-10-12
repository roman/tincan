package taskstate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/stateversion"
)

var ErrStoreLockTimeout = errors.New("task record lock timed out")
var ErrPublicationUnconfirmed = errors.New("task record publication requires reconciliation")
var ErrWorkerConflict = errors.New("worker is already assigned to another task")
var ErrTaskNotFound = errors.New("task record does not exist")

const storeLockTimeout = 2 * time.Second

// Store selects a version 2 task-state root without changing it.
type Store struct {
	path          string
	syncDirectory func(*os.Root) error
}

// NewStore validates the directory path used by all task operations.
func NewStore(path string) (*Store, error) {
	root, err := firstmate.ParseAbsolutePath(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: root.String(), syncDirectory: syncDirectory}, nil
}

// Create writes a task only when its ID has no record.
func (store *Store) Create(ctx context.Context, task Task) error {
	if task.Snapshot().Worker != nil {
		return ErrInvalidTask
	}
	data, err := task.Encode()
	if err != nil {
		return err
	}
	id, err := parseStoreTaskID(task.ID())
	if err != nil {
		return err
	}
	return store.withVersionTwo(ctx, func(root *os.Root) error {
		return withTaskLock(ctx, root, id, func() error {
			records, selected, err := openRecords(root)
			if err != nil {
				return err
			}
			defer records.Close()
			return writeRecord(root, records, selected, id.String(), data, nil, store.syncDirectory)
		})
	})
}

// Read returns one complete task from the selected version 2 root.
func (store *Store) Read(ctx context.Context, taskID string) (Task, error) {
	id, err := parseStoreTaskID(taskID)
	if err != nil {
		return Task{}, err
	}
	var task Task
	err = store.withVersionTwo(ctx, func(root *os.Root) error {
		records, _, err := openRecords(root)
		if err != nil {
			return err
		}
		defer records.Close()
		task, _, err = readRecord(records, id.String())
		return err
	})
	return task, err
}

// List returns every complete task in ID order from the selected version 2 root.
func (store *Store) List(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := store.withVersionTwo(ctx, func(root *os.Root) error {
		var err error
		tasks, err = ListPinned(ctx, root)
		return err
	})
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListPinned reads a root already protected by a version 2 guard.
// The caller must keep the guard open for the entire read.
func ListPinned(ctx context.Context, root *os.Root) ([]Task, error) {
	if root == nil {
		return nil, ErrInvalidTask
	}
	records, _, err := openRecords(root)
	if err != nil {
		return nil, err
	}
	defer records.Close()
	listing, err := records.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := listing.ReadDir(-1)
	closeErr := listing.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := entry.Name()
		if name == migrationOwnerName ||
			(strings.HasPrefix(name, ".writing-") && !strings.HasSuffix(name, ".json")) {
			continue
		}
		id, valid := strings.CutSuffix(name, ".json")
		if !valid {
			return nil, fmt.Errorf("%w: unexpected task record entry %s", ErrInvalidTask, name)
		}
		if _, err := parseStoreTaskID(id); err != nil {
			return nil, fmt.Errorf("%w: invalid task record name %s", ErrInvalidTask, name)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	tasks := make([]Task, 0, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		task, _, err := readRecord(records, id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// Update serializes a task transition and publishes its complete record.
func (store *Store) Update(
	ctx context.Context, taskID string, transition func(Task) (Task, error),
) (Task, error) {
	if transition == nil {
		return Task{}, ErrInvalidTask
	}
	id, err := parseStoreTaskID(taskID)
	if err != nil {
		return Task{}, err
	}
	var updated Task
	err = store.withVersionTwo(ctx, func(root *os.Root) error {
		var updateErr error
		updated, updateErr = store.updatePinned(ctx, root, id, func(current Task) (Task, error) {
			candidate, err := transition(current)
			if err != nil {
				return Task{}, err
			}
			if !sameWorkerIdentity(current, candidate) {
				return Task{}, ErrWorkerConflict
			}
			return candidate, nil
		})
		return updateErr
	})
	if err != nil && !errors.Is(err, ErrPublicationUnconfirmed) {
		return Task{}, err
	}
	return updated, err
}

// ReserveAdoption serializes worker identity claims across all task records.
func (store *Store) ReserveAdoption(
	ctx context.Context, taskID string, worker Worker,
) (Task, error) {
	id, err := parseStoreTaskID(taskID)
	if err != nil {
		return Task{}, err
	}
	var updated Task
	err = store.withVersionTwo(ctx, func(root *os.Root) error {
		return withRootLock(ctx, root, ".worker-adoption.lock", func() error {
			tasks, err := ListPinned(ctx, root)
			if err != nil {
				return err
			}
			updated, err = store.updatePinned(ctx, root, id, func(task Task) (Task, error) {
				candidate, err := task.ReserveAdoption(worker)
				if err != nil {
					return Task{}, err
				}
				if err := checkWorkerIdentity(tasks, candidate); err != nil {
					return Task{}, err
				}
				return candidate, nil
			})
			return err
		})
	})
	if err != nil && !errors.Is(err, ErrPublicationUnconfirmed) {
		return Task{}, err
	}
	return updated, err
}

func sameWorkerIdentity(current, candidate Task) bool {
	previous := current.Snapshot().Worker
	worker := candidate.Snapshot().Worker
	if worker == nil {
		return true
	}
	return previous != nil && previous.Session == worker.Session &&
		previous.Name == worker.Name && previous.Pane == worker.Pane
}

func checkWorkerIdentity(tasks []Task, candidate Task) error {
	worker := candidate.Snapshot().Worker
	if worker == nil {
		return nil
	}
	for _, task := range tasks {
		if task.ID() == candidate.ID() {
			continue
		}
		assigned := task.Snapshot().Worker
		if assigned != nil && assigned.Session == worker.Session &&
			(assigned.Name == worker.Name || assigned.Pane == worker.Pane) {
			return ErrWorkerConflict
		}
	}
	return nil
}

func (store *Store) updatePinned(
	ctx context.Context, root *os.Root, id firstmate.TaskID, transition func(Task) (Task, error),
) (Task, error) {
	return store.withTaskPinned(ctx, root, id, func(task *LockedTask) error {
		_, err := task.apply(transition)
		return err
	})
}

func parseStoreTaskID(value string) (firstmate.TaskID, error) {
	id, err := firstmate.ParseTaskID(value)
	if err != nil || len(value) > maximumTaskIDBytes {
		return firstmate.TaskID{}, ErrInvalidTask
	}
	return id, nil
}

func (store *Store) withVersionTwo(ctx context.Context, operation func(*os.Root) error) error {
	if store == nil || store.path == "" {
		return ErrInvalidTask
	}
	guard, err := stateversion.AcquireVersionTwo(ctx, store.path)
	if err != nil {
		return err
	}
	defer guard.Close()
	return operation(guard.StateRoot())
}

func openRecords(root *os.Root) (*os.Root, os.FileInfo, error) {
	selected, err := root.Lstat("records")
	if err != nil {
		return nil, nil, err
	}
	if !selected.IsDir() {
		return nil, nil, fmt.Errorf("%w: task records path is not a directory", ErrInvalidTask)
	}
	records, err := root.OpenRoot("records")
	if err != nil {
		return nil, nil, err
	}
	opened, err := records.Stat(".")
	if err != nil || !os.SameFile(selected, opened) {
		records.Close()
		return nil, nil, fmt.Errorf(
			"%w: task records directory changed during open",
			ErrInvalidTask,
		)
	}
	return records, selected, nil
}

func withTaskLock(
	ctx context.Context, root *os.Root, id firstmate.TaskID, operation func() error,
) error {
	return withRootLock(ctx, root, ".task-"+id.String()+".lock", operation)
}

func withRootLock(
	ctx context.Context, root *os.Root, name string, operation func() error,
) error {
	lock, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	selected, err := root.Lstat(name)
	if err != nil {
		return err
	}
	opened, err := lock.Stat()
	if err != nil || !selected.Mode().IsRegular() || !os.SameFile(selected, opened) {
		return fmt.Errorf("%w: task record lock is not a regular file", ErrInvalidTask)
	}
	timer := time.NewTimer(storeLockTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			return ErrStoreLockTimeout
		case <-ticker.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

func readRecord(root *os.Root, taskID string) (Task, os.FileInfo, error) {
	name := taskID + ".json"
	selected, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Task{}, nil, errors.Join(ErrTaskNotFound, err)
		}
		return Task{}, nil, err
	}
	if !selected.Mode().IsRegular() {
		return Task{}, nil, fmt.Errorf("%w: task record is not a regular file", ErrInvalidTask)
	}
	file, err := root.Open(name)
	if err != nil {
		return Task{}, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(selected, opened) {
		return Task{}, nil, fmt.Errorf("%w: task record changed during open", ErrInvalidTask)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumTaskBytes+1))
	if err != nil {
		return Task{}, nil, err
	}
	task, err := Decode(data)
	if err != nil || task.ID() != taskID {
		return Task{}, nil, fmt.Errorf("%w: task record does not match %s", ErrInvalidTask, taskID)
	}
	return task, opened, nil
}

func writeRecord(
	stateRoot, records *os.Root, selectedDirectory os.FileInfo,
	taskID string, data []byte, previous os.FileInfo,
	syncDirectory func(*os.Root) error,
) error {
	name := taskID + ".json"
	temporaryName := ".writing-" + rand.Text()
	temporary, err := records.OpenFile(temporaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer records.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
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
	currentDirectory, err := stateRoot.Lstat("records")
	if err != nil {
		return err
	}
	if !os.SameFile(selectedDirectory, currentDirectory) {
		return fmt.Errorf("%w: task records directory changed before publish", ErrInvalidTask)
	}
	currentRecord, err := records.Lstat(name)
	if previous == nil && err == nil {
		return fmt.Errorf("%w: task record already exists", os.ErrExist)
	}
	if previous == nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if previous != nil && (err != nil || !os.SameFile(previous, currentRecord)) {
		return fmt.Errorf("%w: task record changed before publish", ErrInvalidTask)
	}
	if err := records.Rename(temporaryName, name); err != nil {
		return err
	}
	if err := syncDirectory(records); err != nil {
		return errors.Join(ErrPublicationUnconfirmed, err)
	}
	currentDirectory, err = stateRoot.Lstat("records")
	if err != nil {
		return errors.Join(ErrPublicationUnconfirmed, err)
	}
	if !os.SameFile(selectedDirectory, currentDirectory) {
		return fmt.Errorf("%w: records directory changed after publish", ErrPublicationUnconfirmed)
	}
	return nil
}

func syncDirectory(records *os.Root) error {
	directory, err := records.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
