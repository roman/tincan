package firstmate

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	ErrLockTimeout  = errors.New("task metadata lock timed out")
	ErrNotLockOwner = errors.New("task metadata lock is not owned by this caller")
	ErrInvalidLock  = errors.New("invalid task metadata lock")
)

const (
	lockPollInterval = 100 * time.Millisecond
	missingPIDGrace  = 2 * time.Second
)

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
// Dead owners are reclaimed under Firstmate's steal lock.
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
		lock, err := tryCreateTaskLock(path, nil)
		if err != nil {
			return nil, err
		}
		if lock == nil {
			lock, err = recoverTaskLock(path)
			if err != nil {
				return nil, err
			}
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

func tryCreateTaskLock(path string, allowedSteal *TaskLock) (*TaskLock, error) {
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
	stealBlocked := pathExists(path+".steal") &&
		(allowedSteal == nil || allowedSteal.path != path+".steal" || !allowedSteal.owns())
	if !lock.owns() || stealBlocked {
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

// lockObservation pins an owner directory while its PID and lock link are checked.
type lockObservation struct {
	path         string
	pathInfo     os.FileInfo
	linkTarget   string
	ownerPath    string
	ownerInfo    os.FileInfo
	ownerRoot    *os.Root
	ownerMissing bool
	pid          int
}

func observeLock(path string) (*lockObservation, error) {
	pathInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	observation := &lockObservation{path: path, pathInfo: pathInfo, ownerPath: path}
	if pathInfo.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		owner, err := resolvedOwnerPath(path, target)
		if err != nil {
			if !pathSnapshotMatches(path, pathInfo, target) {
				return nil, nil
			}
			return nil, err
		}
		observation.linkTarget = target
		observation.ownerPath = owner
	} else if !pathInfo.IsDir() {
		if !pathSnapshotMatches(path, pathInfo, "") {
			return nil, nil
		}
		return nil, ErrInvalidLock
	}
	ownerInfo, err := os.Lstat(observation.ownerPath)
	if !pathSnapshotMatches(path, pathInfo, observation.linkTarget) {
		return nil, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		observation.ownerMissing = true
		return observation, nil
	}
	if err != nil {
		return nil, err
	}
	if !ownerInfo.IsDir() || ownerInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidLock
	}
	root, err := os.OpenRoot(observation.ownerPath)
	if err != nil {
		if !pathSnapshotMatches(path, pathInfo, observation.linkTarget) ||
			errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(ownerInfo, opened) ||
		!pathSnapshotMatches(path, pathInfo, observation.linkTarget) {
		root.Close()
		return nil, nil
	}
	observation.ownerInfo = ownerInfo
	observation.ownerRoot = root
	observation.pid = (&TaskLock{ownerRoot: root}).ownerPID()
	return observation, nil
}

func pathSnapshotMatches(path string, expected os.FileInfo, target string) bool {
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(expected, current) {
		return false
	}
	if expected.Mode()&os.ModeSymlink == 0 {
		return true
	}
	actual, err := os.Readlink(path)
	return err == nil && actual == target
}

func (observation *lockObservation) close() {
	if observation != nil && observation.ownerRoot != nil {
		_ = observation.ownerRoot.Close()
	}
}

func (observation *lockObservation) unchanged() bool {
	if !pathSnapshotMatches(observation.path, observation.pathInfo, observation.linkTarget) {
		return false
	}
	if observation.ownerMissing {
		_, err := os.Lstat(observation.ownerPath)
		return errors.Is(err, os.ErrNotExist)
	}
	currentOwner, err := os.Lstat(observation.ownerPath)
	if err != nil || !currentOwner.IsDir() ||
		!os.SameFile(observation.ownerInfo, currentOwner) {
		return false
	}
	return observation.pid == (&TaskLock{ownerRoot: observation.ownerRoot}).ownerPID()
}

func (observation *lockObservation) stale() bool {
	if observation == nil || pidAlive(observation.pid) {
		return false
	}
	return observation.pid != 0 || time.Since(observation.pathInfo.ModTime()) >= missingPIDGrace
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func resolvedOwnerPath(path, target string) (string, error) {
	owner := target
	if !filepath.IsAbs(owner) {
		owner = filepath.Join(filepath.Dir(path), owner)
	}
	owner = filepath.Clean(owner)
	if filepath.Dir(owner) != filepath.Dir(path) ||
		!strings.HasPrefix(filepath.Base(owner), filepath.Base(path)+".owner.") ||
		filepath.Base(owner) == filepath.Base(path)+".owner." {
		return "", ErrInvalidLock
	}
	return owner, nil
}

func recoverTaskLock(path string) (*TaskLock, error) {
	initial, err := observeLock(path)
	if err != nil {
		return nil, err
	}
	if initial == nil {
		return nil, nil
	}
	stale := initial.stale()
	initial.close()
	if !stale {
		return nil, nil
	}
	steal, err := acquireStealLock(path + ".steal")
	if err != nil || steal == nil {
		return nil, err
	}
	recovered, recoveryErr := reclaimUnderSteal(path, steal)
	releaseErr := steal.Release()
	if releaseErr != nil {
		if recovered != nil {
			_ = recovered.Release()
		}
		return nil, releaseErr
	}
	return recovered, recoveryErr
}

func acquireStealLock(path string) (*TaskLock, error) {
	lock, err := tryCreateTaskLock(path, nil)
	if err != nil || lock != nil {
		return lock, err
	}
	// A dead nested steal marker can block the claim of this steal lock.
	if _, err := reapDeadLink(path + ".steal"); err != nil {
		return nil, err
	}
	if _, err := reapDeadLink(path); err != nil {
		return nil, err
	}
	return tryCreateTaskLock(path, nil)
}

func reclaimUnderSteal(path string, steal *TaskLock) (*TaskLock, error) {
	if !steal.owns() {
		return nil, ErrNotLockOwner
	}
	observation, err := observeLock(path)
	if err != nil || observation == nil {
		return nil, err
	}
	defer observation.close()
	if !observation.stale() || !observation.unchanged() {
		return nil, nil
	}
	if observation.linkTarget != "" {
		if observation.ownerMissing {
			interrupted, err := ownerTombstoneExists(observation.ownerPath)
			if err != nil {
				return nil, err
			}
			if interrupted {
				reaped, err := reapDeadLink(path)
				if err != nil || !reaped {
					return nil, err
				}
			} else if sameLink(observation) {
				if err := os.Remove(path); err != nil {
					return nil, err
				}
			}
		} else {
			if !sameLink(observation) {
				return nil, nil
			}
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			cleanKnownOwnerFiles(observation.ownerRoot)
			if sameDirectory(observation.ownerPath, observation.ownerInfo) {
				if err := os.Remove(observation.ownerPath); err != nil &&
					!errors.Is(err, syscall.ENOTEMPTY) {
					return nil, err
				}
			}
		}
	} else {
		if err := removeLegacyLock(observation); err != nil {
			return nil, err
		}
	}
	return tryCreateTaskLock(path, steal)
}

func ownerTombstoneExists(owner string) (bool, error) {
	entries, err := os.ReadDir(filepath.Dir(owner))
	if err != nil {
		return false, err
	}
	prefix := filepath.Base(owner) + ".reaped."
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			return true, nil
		}
	}
	return false, nil
}

func removeLegacyLock(observation *lockObservation) error {
	if !observation.unchanged() || !observation.stale() {
		return ErrInvalidLock
	}
	cleanKnownOwnerFiles(observation.ownerRoot)
	if !sameDirectory(observation.path, observation.ownerInfo) {
		return ErrInvalidLock
	}
	if err := os.Remove(observation.path); err != nil && !errors.Is(err, syscall.ENOTEMPTY) {
		return err
	}
	return nil
}

func sameLink(observation *lockObservation) bool {
	info, err := os.Lstat(observation.path)
	if err != nil || !os.SameFile(observation.pathInfo, info) {
		return false
	}
	target, err := os.Readlink(observation.path)
	return err == nil && target == observation.linkTarget
}

func sameDirectory(path string, expected os.FileInfo) bool {
	current, err := os.Lstat(path)
	return err == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 &&
		os.SameFile(expected, current)
}

func cleanKnownOwnerFiles(root *os.Root) {
	for _, name := range []string{"pid", "fm-home", "pid-identity", "role", "watcher-path"} {
		_ = root.Remove(name)
	}
}

type reaperGate struct {
	mutex      sync.Mutex
	references int
}

var reaperGates = struct {
	sync.Mutex
	byPath map[string]*reaperGate
}{byPath: make(map[string]*reaperGate)}

// tryLockReaper coordinates Go reapers for one lock path without extending
// acquisition waits. The filesystem steal lock still coordinates processes.
func tryLockReaper(path string) (func(), bool) {
	reaperGates.Lock()
	gate := reaperGates.byPath[path]
	if gate == nil {
		gate = &reaperGate{}
		reaperGates.byPath[path] = gate
	}
	gate.references++
	reaperGates.Unlock()

	if !gate.mutex.TryLock() {
		releaseReaperReference(path, gate)
		return nil, false
	}
	return func() {
		gate.mutex.Unlock()
		releaseReaperReference(path, gate)
	}, true
}

func releaseReaperReference(path string, gate *reaperGate) {
	reaperGates.Lock()
	gate.references--
	if gate.references == 0 {
		delete(reaperGates.byPath, path)
	}
	reaperGates.Unlock()
}

// reapDeadLink elects one reaper by atomically moving a dead owner directory.
// A dead reaper's tombstone can be re-elected. Live reapers and ambiguous
// tombstones remain untouched.
func reapDeadLink(path string) (bool, error) {
	releaseReaper, acquired := tryLockReaper(path)
	if !acquired {
		return false, nil
	}
	defer releaseReaper()

	observation, err := observeLock(path)
	if err != nil || observation == nil {
		return false, err
	}
	defer observation.close()
	if observation.linkTarget == "" {
		return false, nil
	}
	owner := observation.ownerPath
	token := owner
	if observation.ownerMissing {
		var found bool
		token, found, err = deadOwnerTombstone(owner)
		if err != nil || !found {
			return false, err
		}
	} else if !observation.stale() || !observation.unchanged() {
		return false, nil
	}
	tomb := owner + ".reaped." + strconv.Itoa(os.Getpid())
	if token != tomb {
		if pathExists(tomb) {
			return false, nil
		}
		if err := os.Rename(token, tomb); err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrExist) {
				return false, nil
			}
			return false, err
		}
	}
	if sameLink(observation) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if err := discardPinnedOwner(tomb); err != nil {
		return false, err
	}
	return true, nil
}

func deadOwnerTombstone(owner string) (string, bool, error) {
	entries, err := os.ReadDir(filepath.Dir(owner))
	if err != nil {
		return "", false, err
	}
	prefix := filepath.Base(owner) + ".reaped."
	var candidate string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), prefix))
		if err != nil || pid <= 0 || !entry.IsDir() {
			return "", false, ErrInvalidLock
		}
		if pid != os.Getpid() && pidAlive(pid) {
			return "", false, nil
		}
		if candidate != "" {
			return "", false, ErrInvalidLock
		}
		candidate = filepath.Join(filepath.Dir(owner), entry.Name())
	}
	return candidate, candidate != "", nil
}

func discardPinnedOwner(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidLock
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return ErrInvalidLock
	}
	cleanKnownOwnerFiles(root)
	if !sameDirectory(path, info) {
		return ErrInvalidLock
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, syscall.ENOTEMPTY) &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
