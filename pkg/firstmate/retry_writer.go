package firstmate

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var ErrRetryWriterBusy = errors.New("retry state has another Tincan writer")

// RetryWriter owns the canonical state directory and its writer lock.
type RetryWriter struct {
	StateDir AbsolutePath
	lock     *os.File
}

func (writer *RetryWriter) Release() error {
	if err := syscall.Flock(int(writer.lock.Fd()), syscall.LOCK_UN); err != nil {
		_ = writer.lock.Close()
		return err
	}
	return writer.lock.Close()
}

// AcquireRetryWriter claims the local retry-state writer until Release.
func AcquireRetryWriter(stateDir AbsolutePath) (*RetryWriter, error) {
	if stateDir.value == "" {
		return nil, ErrInvalidPath
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
	lock, err := os.OpenFile(
		filepath.Join(state, ".tincan-retry-writer.lock"),
		os.O_CREATE|os.O_RDWR,
		0600,
	)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrRetryWriterBusy
		}
		return nil, err
	}
	return &RetryWriter{StateDir: AbsolutePath{value: state}, lock: lock}, nil
}
