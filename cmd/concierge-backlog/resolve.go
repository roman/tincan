package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
)

type resolveResult struct {
	TaskID  string
	State   string
	Already bool
}

func resolveTask(
	root, backlogFile, messageRoot, replyID, requestedMode string,
) (resolveResult, error) {
	mode := crew.CompletionMode(requestedMode)
	if mode != crew.CloseTask && mode != crew.RetainTask {
		return resolveResult{}, fmt.Errorf("invalid completion mode %q", requestedMode)
	}
	id, err := inbox.ParseMessageID(replyID)
	if err != nil {
		return resolveResult{}, err
	}
	store, err := inbox.New(messageRoot)
	if err != nil {
		return resolveResult{}, err
	}
	primary, _ := inbox.ParseRecipient("firstmate")
	message, err := store.Read(primary, id)
	if errors.Is(err, os.ErrNotExist) {
		if _, dispositionErr := store.ReadDisposition(primary, id); dispositionErr != nil {
			return resolveResult{}, fmt.Errorf(
				"handled reply has no resolution: %w",
				dispositionErr,
			)
		}
		message, err = store.ReadHandled(primary, id)
	}
	if err != nil {
		return resolveResult{}, err
	}
	reply, err := taskprotocol.DecodeReply(message.Body, message.Key, root)
	if err != nil {
		return resolveResult{}, err
	}
	if reply.AssignmentID == "" {
		return resolveResult{}, errors.New("task resolution requires a bound assignment")
	}
	assignmentID, _ := inbox.ParseMessageID(reply.AssignmentID)
	if err := taskprotocol.ValidateBoundAssignment(store, root, reply.Task,
		reply.SpawnGeneration, assignmentID); err != nil {
		return resolveResult{}, err
	}
	action := fmt.Sprintf("finish:%s:%s", reply.Task, reply.SpawnGeneration)
	disposition, _, err := store.BeginDisposition(primary, id, action)
	if err != nil {
		return resolveResult{}, err
	}
	state := "done"
	if mode == crew.RetainTask {
		state = "queued"
	}
	result := resolveResult{TaskID: reply.Task, State: state}
	if disposition.State == inbox.DispositionComplete {
		if disposition.Outcome != state {
			return resolveResult{}, inbox.ErrDispositionConflict
		}
		result.Already = true
	} else {
		finished, err := finishTask(root, backlogFile, finishRequest{
			TaskID: reply.Task, Mode: mode, SpawnGeneration: reply.SpawnGeneration,
		})
		if err != nil {
			if !errors.Is(err, crew.ErrStaleRecord) ||
				!completedTaskAfterCleanup(root, backlogFile, reply.Task,
					reply.SpawnGeneration, mode, state) {
				return resolveResult{}, err
			}
			result.Already = true
		} else {
			result.Already = finished.Already
		}
		if _, err := store.CompleteDisposition(primary, id, state); err != nil {
			return resolveResult{}, err
		}
	}
	if err := store.Acknowledge(primary, id); err != nil {
		return resolveResult{}, err
	}
	return result, nil
}

func completedTaskAfterCleanup(
	root, backlogFile, taskID, generation string, mode crew.CompletionMode, expectedState string,
) bool {
	registry, err := crew.New(root)
	if err != nil {
		return false
	}
	err = registry.WithRetiredReceipt(taskID, func(receipt crew.CompletionReceipt) error {
		if receipt.SpawnGeneration != generation || receipt.Mode != mode {
			return crew.ErrStaleRecord
		}
		unlock, err := lockBacklog(backlogFile)
		if err != nil {
			return err
		}
		row, rowErr := showBacklogRow(backlogFile, taskID)
		unlockErr := unlock()
		if err := errors.Join(rowErr, unlockErr); err != nil {
			return err
		}
		if row.State != expectedState {
			return crew.ErrStaleRecord
		}
		return nil
	})
	return err == nil
}
