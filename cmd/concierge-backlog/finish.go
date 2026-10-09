package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

type finishResult struct {
	State   string
	Already bool
}

func finishTask(
	ctx context.Context, root, taskID, backlogFile, requestedMode string, client herdr.Client,
) (finishResult, error) {
	if _, err := firstmate.ParseTaskID(taskID); err != nil {
		return finishResult{}, err
	}
	mode := crew.CompletionMode(requestedMode)
	if mode != crew.CloseTask && mode != crew.RetainTask {
		return finishResult{}, fmt.Errorf("invalid completion mode %q", requestedMode)
	}
	version, err := exec.Command("tasks-axi", "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "0.2.6" {
		return finishResult{}, fmt.Errorf("backlog finish requires tasks-axi 0.2.6")
	}
	registry, err := crew.New(root)
	if err != nil {
		return finishResult{}, err
	}
	result := finishResult{State: "done"}
	if mode == crew.RetainTask {
		result.State = "queued"
	}
	err = registry.WithCompletion(taskID, func(completion *crew.Completion) (transitionErr error) {
		unlock, err := lockBacklog(backlogFile)
		if err != nil {
			return err
		}
		lockHeld := true
		defer func() {
			if lockHeld {
				transitionErr = errors.Join(transitionErr, unlock())
			}
		}()
		release := func() error {
			lockHeld = false
			return unlock()
		}
		row, err := showBacklogRow(backlogFile, taskID)
		if err != nil {
			return err
		}
		if completion.Pending() && completion.Mode() != mode {
			return fmt.Errorf("pending completion mode is %s", completion.Mode())
		}
		if completion.Pending() && row.State == result.State {
			if err := completion.Retire(ctx, client, mode); err != nil {
				return err
			}
			if err := release(); err != nil {
				return err
			}
			if err := completion.Clear(); err != nil {
				return err
			}
			result.Already = true
			return nil
		}
		if row.State != "in_flight" {
			return fmt.Errorf("task is %s; expected in_flight", row.State)
		}
		if mode == crew.CloseTask && row.Held == "yes" {
			return fmt.Errorf("task is held; retain it or release the hold before closing")
		}
		if mode == crew.RetainTask && row.Held != "yes" && !completion.Pending() {
			return fmt.Errorf("retention requires a held task")
		}
		if err := completion.Retire(ctx, client, mode); err != nil {
			return err
		}
		staged, selected, err := stageBacklog(backlogFile)
		if err != nil {
			return err
		}
		defer os.RemoveAll(filepath.Dir(staged))
		args := []string{"done", taskID, "--no-prune", "--file", staged, "--json"}
		if mode == crew.RetainTask {
			args = []string{"reopen", taskID, "--file", staged, "--json"}
		}
		output, err := exec.Command("tasks-axi", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("tasks-axi %s failed: %w: %s", args[0], err, output)
		}
		confirmed, err := showBacklogRow(staged, taskID)
		if err != nil {
			return err
		}
		if confirmed.State != result.State {
			return fmt.Errorf("task remains %s after tasks-axi %s", confirmed.State, args[0])
		}
		if err := syncStagedBacklog(staged, selected.Mode().Perm()); err != nil {
			return err
		}
		current, err := os.Lstat(backlogFile)
		if err != nil {
			return fmt.Errorf("backlog changed during finish: %w", err)
		}
		if !os.SameFile(selected, current) {
			return fmt.Errorf("backlog changed during finish")
		}
		if err := completion.CheckCurrent(); err != nil {
			return err
		}
		if err := os.Rename(staged, backlogFile); err != nil {
			return err
		}
		if err := syncBacklogDirectory(backlogFile); err != nil {
			return err
		}
		if err := release(); err != nil {
			return err
		}
		return completion.Clear()
	})
	if err != nil {
		return finishResult{}, fmt.Errorf("task completion or backlog transition: %w", err)
	}
	return result, nil
}
