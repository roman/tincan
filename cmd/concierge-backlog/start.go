package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
)

type startResult struct {
	Already bool
}

type backlogRow struct {
	ID      string
	State   string
	Blocked string
	Held    string
}

func startTask(root, taskID, backlogFile string) (startResult, error) {
	if _, err := firstmate.ParseTaskID(taskID); err != nil {
		return startResult{}, err
	}
	version, err := exec.Command("tasks-axi", "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "0.2.6" {
		return startResult{}, fmt.Errorf("backlog start requires tasks-axi 0.2.6")
	}
	registry, err := crew.New(root)
	if err != nil {
		return startResult{}, err
	}
	var result startResult
	err = registry.WithCurrentTask(taskID, func(*crew.CurrentTask) (transitionErr error) {
		unlock, err := lockBacklog(backlogFile)
		if err != nil {
			return err
		}
		defer func() { transitionErr = errors.Join(transitionErr, unlock()) }()
		row, err := showBacklogRow(backlogFile, taskID)
		if err != nil {
			return err
		}
		if row.State == "in_flight" {
			result.Already = true
			return nil
		}
		if row.State != "queued" {
			return fmt.Errorf("task is %s; expected queued", row.State)
		}
		if row.Blocked != "no" {
			return fmt.Errorf("task is blocked by an unfinished dependency")
		}
		if row.Held != "no" {
			return fmt.Errorf("task is held")
		}
		staged, selected, err := stageBacklog(backlogFile)
		if err != nil {
			return err
		}
		defer os.RemoveAll(filepath.Dir(staged))
		output, err := exec.Command("tasks-axi", "start", taskID, "--file", staged, "--json").
			CombinedOutput()
		if err != nil {
			return fmt.Errorf("tasks-axi start failed: %w: %s", err, output)
		}
		confirmed, err := showBacklogRow(staged, taskID)
		if err != nil {
			return err
		}
		if confirmed.State != "in_flight" {
			return fmt.Errorf("task remains %s after tasks-axi start", confirmed.State)
		}
		if err := syncStagedBacklog(staged, selected.Mode().Perm()); err != nil {
			return err
		}
		current, err := os.Lstat(backlogFile)
		if err != nil {
			return fmt.Errorf("backlog changed during start: %w", err)
		}
		if !os.SameFile(selected, current) {
			return fmt.Errorf("backlog changed during start")
		}
		if err := os.Rename(staged, backlogFile); err != nil {
			return err
		}
		directory, err := os.Open(filepath.Dir(backlogFile))
		if err != nil {
			return err
		}
		defer directory.Close()
		return directory.Sync()
	})
	if err != nil {
		return startResult{}, fmt.Errorf(
			"current task worker binding or backlog transition: %w",
			err,
		)
	}
	return result, nil
}

func syncStagedBacklog(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func stageBacklog(backlogFile string) (string, os.FileInfo, error) {
	selected, err := os.Lstat(backlogFile)
	if err != nil {
		return "", nil, err
	}
	if !selected.Mode().IsRegular() {
		return "", nil, fmt.Errorf("backlog is not a regular file")
	}
	source, err := os.Open(backlogFile)
	if err != nil {
		return "", nil, err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("backlog changed during open: %w", err)
	}
	if !os.SameFile(selected, opened) {
		return "", nil, fmt.Errorf("backlog changed during open")
	}
	stagingDirectory, err := os.MkdirTemp(filepath.Dir(backlogFile), ".backlog-stage-*")
	if err != nil {
		return "", nil, err
	}
	name := filepath.Join(stagingDirectory, "backlog.md")
	staged, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		os.RemoveAll(stagingDirectory)
		return "", nil, err
	}
	defer func() {
		staged.Close()
		if err != nil {
			os.RemoveAll(stagingDirectory)
		}
	}()
	if err = staged.Chmod(selected.Mode().Perm()); err != nil {
		return "", nil, err
	}
	if _, err = io.Copy(staged, source); err != nil {
		return "", nil, err
	}
	if err = staged.Sync(); err != nil {
		return "", nil, err
	}
	if err = staged.Close(); err != nil {
		return "", nil, err
	}
	return name, selected, nil
}

func showBacklogRow(backlogFile, taskID string) (backlogRow, error) {
	output, err := exec.Command("tasks-axi", "show", taskID, "--file", backlogFile).CombinedOutput()
	if err != nil {
		return backlogRow{}, fmt.Errorf("tasks-axi show failed: %w: %s", err, output)
	}
	var row backlogRow
	for _, line := range strings.Split(string(output), "\n") {
		switch {
		case strings.HasPrefix(line, "  id: ") && row.ID == "":
			row.ID = strings.TrimPrefix(line, "  id: ")
		case strings.HasPrefix(line, "  state: ") && row.State == "":
			row.State = strings.TrimPrefix(line, "  state: ")
		case strings.HasPrefix(line, "  blocked: ") && row.Blocked == "":
			row.Blocked = strings.TrimPrefix(line, "  blocked: ")
		case strings.HasPrefix(line, "  held: ") && row.Held == "":
			row.Held = strings.TrimPrefix(line, "  held: ")
		}
	}
	if row.ID != taskID || row.State == "" || row.Blocked == "" || row.Held == "" {
		return backlogRow{}, fmt.Errorf("invalid tasks-axi show response for %s", taskID)
	}
	return row, nil
}
