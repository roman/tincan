package firstmate

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrLockTimeout  = errors.New("task metadata lock timed out")
	ErrNotLockOwner = errors.New("task metadata lock is not owned by this caller")
	ErrInvalidLock  = errors.New("invalid task metadata lock")
)

const lockPollInterval = 100 * time.Millisecond

// TaskLock holds one Firstmate-compatible task metadata lock.
type TaskLock struct {
	path         string
	ownerPath    string
	ownerRoot    *os.Root
	ownerInfo    os.FileInfo
	releaseState *taskLockReleaseState
}

type taskLockReleaseState struct {
	sync.Mutex
	released bool
}

// AcquireTaskLock waits at most timeout to claim the task's metadata lock.
// Existing locks, including abandoned ones, remain untouched and cause a timeout.
func AcquireTaskLock(stateDir AbsolutePath, id TaskID, timeout time.Duration) (*TaskLock, error) {
	if stateDir.value == "" {
		return nil, ErrInvalidPath
	}
	if id.value == "" {
		return nil, ErrInvalidTaskID
	}
	if timeout <= 0 {
		return nil, ErrLockTimeout
	}
	state, err := filepath.EvalSymlinks(stateDir.value)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(state)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrInvalidPath
	}
	path := filepath.Join(state, ".meta-"+id.value+".lock")
	deadline := time.Now().Add(timeout)
	for {
		lock, err := tryCreateTaskLock(path)
		if err != nil {
			return nil, err
		}
		if lock != nil {
			return lock, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ErrLockTimeout
		}
		if remaining > lockPollInterval {
			remaining = lockPollInterval
		}
		time.Sleep(remaining)
	}
}

// Release removes the link only while this process still owns its original directory.
func (lock *TaskLock) Release() error {
	if lock == nil || lock.releaseState == nil {
		return ErrNotLockOwner
	}
	lock.releaseState.Lock()
	defer lock.releaseState.Unlock()
	if lock.releaseState.released {
		return ErrNotLockOwner
	}
	if !lock.owns() {
		lock.releaseState.released = true
		_ = lock.ownerRoot.Close()
		return ErrNotLockOwner
	}
	if err := os.Remove(lock.path); err != nil {
		return err
	}
	lock.releaseState.released = true
	return lock.discardOwner()
}

func tryCreateTaskLock(path string) (*TaskLock, error) {
	lock, err := createOwner(path)
	if err != nil {
		return nil, err
	}
	if err := os.Symlink(lock.ownerPath, path); err != nil {
		_ = lock.discardOwner()
		if errors.Is(err, os.ErrExist) {
			return nil, nil
		}
		return nil, err
	}
	if !lock.owns() || pathExists(path+".steal") {
		if pointsTo(path, lock.ownerPath) {
			_ = os.Remove(path)
		}
		_ = lock.discardOwner()
		return nil, nil
	}
	return lock, nil
}

func createOwner(path string) (*TaskLock, error) {
	ownerPath, err := os.MkdirTemp(filepath.Dir(path), filepath.Base(path)+".owner.*")
	if err != nil {
		return nil, err
	}
	ownerInfo, err := os.Lstat(ownerPath)
	if err != nil {
		return nil, err
	}
	ownerRoot, err := os.OpenRoot(ownerPath)
	if err != nil {
		return nil, err
	}
	lock := &TaskLock{
		path: path, ownerPath: ownerPath, ownerRoot: ownerRoot, ownerInfo: ownerInfo,
		releaseState: &taskLockReleaseState{},
	}
	if !lock.sameOwnerDirectory() {
		_ = ownerRoot.Close()
		return nil, ErrInvalidLock
	}
	pid := strconv.Itoa(os.Getpid()) + "\n"
	if err := ownerRoot.WriteFile("pid", []byte(pid), 0600); err != nil {
		_ = lock.discardOwner()
		return nil, err
	}
	if lock.ownerPID() != os.Getpid() {
		_ = lock.discardOwner()
		return nil, ErrInvalidLock
	}
	return lock, nil
}

func (lock *TaskLock) owns() bool {
	return lock.sameOwnerDirectory() &&
		lock.ownerPID() == os.Getpid() &&
		pointsTo(lock.path, lock.ownerPath)
}

func (lock *TaskLock) sameOwnerDirectory() bool {
	if lock == nil || lock.ownerRoot == nil || lock.ownerInfo == nil {
		return false
	}
	current, err := os.Lstat(lock.ownerPath)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return false
	}
	opened, err := lock.ownerRoot.Stat(".")
	return err == nil && os.SameFile(lock.ownerInfo, current) &&
		os.SameFile(lock.ownerInfo, opened)
}

func (lock *TaskLock) ownerPID() int {
	info, err := lock.ownerRoot.Lstat("pid")
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	data, err := lock.ownerRoot.ReadFile("pid")
	if err != nil {
		return 0
	}
	value := strings.TrimSuffix(string(data), "\n")
	if value == "" {
		return 0
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0
		}
	}
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func (lock *TaskLock) discardOwner() error {
	defer lock.ownerRoot.Close()
	_ = lock.ownerRoot.Remove("pid")
	if !lock.sameOwnerDirectory() {
		return ErrNotLockOwner
	}
	return os.Remove(lock.ownerPath)
}

func pointsTo(path, owner string) bool {
	actual, err := os.Readlink(path)
	return err == nil && actual == owner
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}
