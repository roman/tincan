package firstmate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidLeaseActor = errors.New("invalid Firstmate supervision actor")
	ErrLeaseConflict     = errors.New("task has a live lease held by another actor")
)

// LeaseActor identifies one of the two Firstmate supervision actors.
type LeaseActor struct{ value string }

// ParseLeaseActor validates an actor. An empty value defaults to main.
func ParseLeaseActor(value string) (LeaseActor, error) {
	if value == "" {
		value = "main"
	}
	if value != "main" && value != "branch" {
		return LeaseActor{}, ErrInvalidLeaseActor
	}
	return LeaseActor{value: value}, nil
}

// String returns the validated actor name.
func (actor LeaseActor) String() string { return actor.value }

// WithLeaseGuard serializes a task mutation against Firstmate lease claims.
// The command lock remains held until the callback returns.
func WithLeaseGuard(
	ctx context.Context,
	stateDir AbsolutePath,
	id TaskID,
	actor LeaseActor,
	callback func() error,
) (result error) {
	if stateDir.value == "" {
		return ErrInvalidPath
	}
	if id.value == "" {
		return ErrInvalidTaskID
	}
	if actor.value != "main" && actor.value != "branch" {
		return ErrInvalidLeaseActor
	}
	if callback == nil {
		return errors.New("nil lease guard callback")
	}
	state, err := filepath.EvalSymlinks(stateDir.value)
	if err != nil {
		return err
	}
	info, err := os.Stat(state)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrInvalidPath
	}
	commandLock, err := acquireLeaseCommandLock(ctx, filepath.Join(state, ".fm-lease-command.lock"))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, commandLock.Release()) }()

	leasePath := filepath.Join(state, ".lease-"+id.value)
	leaseActor, live, exists, err := readLiveLease(leasePath, filepath.Join(state, ".lock"))
	if err != nil {
		return err
	}
	if live && leaseActor != actor.value {
		return ErrLeaseConflict
	}
	if exists && !live {
		if err := os.Remove(leasePath); err != nil {
			return err
		}
	}
	return callback()
}

func acquireLeaseCommandLock(ctx context.Context, path string) (*TaskLock, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := tryAcquirePathLock(path)
		if err != nil {
			return nil, err
		}
		if lock != nil {
			if err := ctx.Err(); err != nil {
				return nil, errors.Join(err, lock.Release())
			}
			return lock, nil
		}
		timer := time.NewTimer(lockPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func readLiveLease(path, sessionLockPath string) (actor string, live, exists bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, true, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, true, err
	}
	fields := strings.Split(strings.SplitN(string(data), "\n", 2)[0], "\t")
	if len(fields) < 2 || (fields[0] != "main" && fields[0] != "branch") {
		return "", false, true, nil
	}
	pid, valid := numericPID(fields[1])
	if !valid || !pidAlive(pid) {
		return "", false, true, nil
	}
	sessionData, err := os.ReadFile(sessionLockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, true, nil
		}
		return "", false, true, err
	}
	sessionPIDText := strings.SplitN(string(sessionData), "\n", 2)[0]
	sessionPID, valid := numericPID(sessionPIDText)
	if !valid || sessionPID <= 1 || fields[1] != sessionPIDText {
		return "", false, true, nil
	}
	return fields[0], true, true, nil
}

func numericPID(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	pid, err := strconv.Atoi(value)
	return pid, err == nil && pid > 0
}
