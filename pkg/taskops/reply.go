package taskops

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
)

// ReplyResult describes the queue outcome of one bound worker reply.
type ReplyResult struct {
	TaskID  string
	State   taskstate.State
	Already bool
}

// Resolve accepts a bound reply before acknowledging its Tincan message.
func (service *Service) Resolve(
	ctx context.Context, messageRoot, replyID string,
	mode taskstate.CompletionMode, closedOn string,
) (ReplyResult, error) {
	return service.resolve(ctx, messageRoot, replyID, mode, closedOn, service.Finish)
}

func (service *Service) resolve(
	ctx context.Context,
	messageRoot, replyID string,
	mode taskstate.CompletionMode,
	closedOn string,
	finish func(context.Context, string, string, taskstate.CompletionMode, string) (taskstate.Task, error),
) (ReplyResult, error) {
	state, err := resultState(mode)
	if err != nil {
		return ReplyResult{}, err
	}
	id, err := inbox.ParseMessageID(replyID)
	if err != nil {
		return ReplyResult{}, err
	}
	messages, err := inbox.New(messageRoot)
	if err != nil {
		return ReplyResult{}, err
	}
	concierge := service.primaryRecipient
	message, err := messages.Read(concierge, id)
	if errors.Is(err, os.ErrNotExist) {
		if _, dispositionErr := messages.ReadDisposition(concierge, id); dispositionErr != nil {
			return ReplyResult{}, fmt.Errorf("handled reply has no resolution: %w", dispositionErr)
		}
		message, err = messages.ReadHandled(concierge, id)
	}
	if err != nil {
		return ReplyResult{}, err
	}
	reply, err := taskprotocol.DecodeReply(message.Body, message.Key, service.root)
	if err != nil {
		return ReplyResult{}, err
	}
	if reply.AssignmentID == "" {
		return ReplyResult{}, taskprotocol.ErrInvalidReply
	}
	assignmentID, _ := inbox.ParseMessageID(reply.AssignmentID)
	if err := taskprotocol.ValidateBoundAssignment(messages, service.root, reply.Task,
		reply.SpawnGeneration, assignmentID); err != nil {
		return ReplyResult{}, err
	}
	action := fmt.Sprintf("finish:%s:%s:%s", reply.Task, reply.SpawnGeneration, mode)
	if mode == taskstate.Close {
		action += ":" + closedOn
	}
	existing, dispositionErr := messages.ReadDisposition(concierge, id)
	if dispositionErr == nil && existing.Action != action {
		return ReplyResult{}, inbox.ErrDispositionConflict
	}
	if dispositionErr != nil && !errors.Is(dispositionErr, os.ErrNotExist) {
		return ReplyResult{}, dispositionErr
	}
	current, err := service.store.Read(ctx, reply.Task)
	if err != nil {
		return ReplyResult{}, err
	}
	if (mode == taskstate.Retain && closedOn != "") ||
		(mode == taskstate.Close && (closedOn == "" ||
			(resultAlreadyApplied(current, reply.SpawnGeneration, mode) &&
				current.Definition().Closed != closedOn))) {
		return ReplyResult{}, taskstate.ErrInvalidTransition
	}
	if errors.Is(dispositionErr, os.ErrNotExist) {
		if !current.HasReceipt(reply.SpawnGeneration, mode) {
			if _, err := current.AcceptResult(reply.SpawnGeneration, mode, closedOn); err != nil {
				return ReplyResult{}, err
			}
		}
	}
	disposition, _, err := messages.BeginDisposition(concierge, id, action)
	if err != nil {
		return ReplyResult{}, err
	}
	result := ReplyResult{TaskID: reply.Task, State: state}
	if disposition.State == inbox.DispositionComplete {
		if disposition.Outcome != string(state) {
			return ReplyResult{}, inbox.ErrDispositionConflict
		}
		result.Already = true
	} else {
		result.Already = resultAlreadyApplied(current, reply.SpawnGeneration, mode)
		if current.HasReceipt(reply.SpawnGeneration, mode) {
			if err := taskview.Rebuild(ctx, service.root); err != nil {
				return result, err
			}
		} else {
			updated, err := finish(ctx, reply.Task, reply.SpawnGeneration, mode, closedOn)
			if err != nil {
				if updated.ID() == "" {
					return ReplyResult{}, err
				}
				return result, err
			}
		}
		if _, err := messages.CompleteDisposition(concierge, id, string(state)); err != nil {
			return result, err
		}
	}
	if err := messages.Acknowledge(concierge, id); err != nil {
		return result, err
	}
	return result, nil
}

func resultState(mode taskstate.CompletionMode) (taskstate.State, error) {
	switch mode {
	case taskstate.Close:
		return taskstate.Done, nil
	case taskstate.Retain:
		return taskstate.Queued, nil
	default:
		return "", taskstate.ErrInvalidTransition
	}
}

func resultAlreadyApplied(
	task taskstate.Task,
	generation string,
	mode taskstate.CompletionMode,
) bool {
	if task.HasReceipt(generation, mode) {
		return true
	}
	expected, _ := resultState(mode)
	if task.State() != expected {
		return false
	}
	snapshot := task.Snapshot()
	if snapshot.Completion != nil {
		return snapshot.Completion.Generation == generation && snapshot.Completion.Mode == mode
	}
	return false
}
