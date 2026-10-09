package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const staleBacklogLockAge = 30 * time.Second

func lockBacklog(backlogFile string) (func() error, error) {
	lockFile := backlogFile + ".lock"
	selected, err := publishBacklogLock(lockFile)
	if errors.Is(err, os.ErrExist) {
		recovered, recoveryErr := recoverDeadTincanLock(lockFile)
		if recoveryErr != nil {
			return nil, recoveryErr
		}
		if recovered {
			selected, err = publishBacklogLock(lockFile)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("backlog is locked: %w", err)
	}
	return func() error {
		current, err := os.Lstat(lockFile)
		if err != nil {
			return err
		}
		if !os.SameFile(selected, current) {
			return fmt.Errorf("backlog lock changed during start")
		}
		if err := os.Remove(lockFile); err != nil {
			return err
		}
		return syncBacklogDirectory(lockFile)
	}, nil
}

func publishBacklogLock(lockFile string) (os.FileInfo, error) {
	file, err := os.CreateTemp(filepath.Dir(lockFile), ".backlog-owner-*")
	if err != nil {
		return nil, err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := fmt.Fprintf(file, "%d:tincan-start\n", os.Getpid()); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	selected, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(temporary, lockFile); err != nil {
		return nil, err
	}
	return selected, nil
}

func recoverDeadTincanLock(lockFile string) (bool, error) {
	recoveryFile, err := os.OpenFile(lockFile+".recovery", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer recoveryFile.Close()
	if err := syscall.Flock(int(recoveryFile.Fd()), syscall.LOCK_EX); err != nil {
		return false, err
	}
	defer syscall.Flock(int(recoveryFile.Fd()), syscall.LOCK_UN)
	selected, err := os.Lstat(lockFile)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !selected.Mode().IsRegular() || time.Since(selected.ModTime()) < staleBacklogLockAge ||
		selected.Size() > 64 {
		return false, nil
	}
	file, err := os.Open(lockFile)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(selected, opened) {
		return false, fmt.Errorf("backlog lock changed during open: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil {
		return false, err
	}
	owner, valid := strings.CutSuffix(string(data), ":tincan-start\n")
	if !valid {
		return false, nil
	}
	pid, err := strconv.Atoi(owner)
	if err != nil || pid <= 0 || !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return false, nil
	}
	current, err := os.Lstat(lockFile)
	if err != nil || !os.SameFile(selected, current) {
		return false, fmt.Errorf("backlog lock changed during recovery: %w", err)
	}
	if err := os.Remove(lockFile); err != nil {
		return false, err
	}
	return true, syncBacklogDirectory(lockFile)
}

func syncBacklogDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
