package resident

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/firstmate"
	"golang.org/x/sys/unix"
)

var ErrAlreadyRunning = errors.New("resident controller already owns this task")

type TaskLease struct{ file *os.File }

// AcquireTaskLease excludes a second controller for the same task.
func AcquireTaskLease(root, taskID string) (*TaskLease, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, firstmate.ErrInvalidPath
	}
	if _, err := firstmate.ParseTaskID(taskID); err != nil {
		return nil, err
	}
	directory, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	descriptor, err := unix.Openat(
		int(directory.Fd()), taskID+".controller.lock",
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), taskID+".controller.lock")
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return &TaskLease{file: file}, nil
}

func (lease *TaskLease) Close() error {
	if lease == nil || lease.file == nil {
		return nil
	}
	err := unix.Flock(int(lease.file.Fd()), unix.LOCK_UN)
	return errors.Join(err, lease.file.Close())
}
