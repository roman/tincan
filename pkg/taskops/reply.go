package taskops

import (
	"context"
	"errors"
	"fmt"
	"github.com/roman/tincan/pkg/firstmate"
	"os"
	"strings"

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
	return service.resolve(
		ctx,
		messageRoot,
		replyID,
		mode,
		closedOn,
		func(task *taskstate.LockedTask, generation string, mode taskstate.CompletionMode, closedOn string) (taskstate.Task, error) {
			updated, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) {
				return current.AcceptResult(generation, mode, closedOn)
			})
			if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
				return taskstate.Task{}, err
			}
			return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
		},
	)
}

func (service *Service) resolve(
	ctx context.Context,
	messageRoot, replyID string,
	mode taskstate.CompletionMode,
	closedOn string,
	finish func(*taskstate.LockedTask, string, taskstate.CompletionMode, string) (taskstate.Task, error),
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

	var result ReplyResult
	_, err = service.store.WithTask(ctx, reply.Task, func(task *taskstate.LockedTask) error {
		current := task.Current()
		generation := reply.SpawnGeneration
		existing, dispositionErr := messages.ReadDisposition(concierge, id)
		suffix := ":" + string(mode)
		if mode == taskstate.Close {
			suffix += ":" + closedOn
		}
		prefix := "finish:" + reply.Task + ":"
		if dispositionErr == nil {
			if !strings.HasPrefix(existing.Action, prefix) ||
				!strings.HasSuffix(existing.Action, suffix) {
				return inbox.ErrDispositionConflict
			}
			generation = strings.TrimSuffix(strings.TrimPrefix(existing.Action, prefix), suffix)
			if firstmate.ValidateSpawnGeneration(generation) != nil {
				return inbox.ErrDispositionConflict
			}
		} else if !errors.Is(dispositionErr, os.ErrNotExist) {
			return dispositionErr
		} else if worker, err := assignmentWorker(current); err == nil && preservedReply(worker, replyID) {
			generation = worker.Generation
		}
		action := fmt.Sprintf("finish:%s:%s%s", reply.Task, generation, suffix)
		if (mode == taskstate.Retain && closedOn != "") ||
			(mode == taskstate.Close && (closedOn == "" || (resultAlreadyApplied(current, generation, mode) && current.Definition().Closed != closedOn))) {
			return taskstate.ErrInvalidTransition
		}
		if errors.Is(dispositionErr, os.ErrNotExist) {
			if !current.HasReceipt(generation, mode) {
				if _, err := current.AcceptResult(generation, mode, closedOn); err != nil {
					return err
				}
			}
		}
		disposition, _, err := messages.BeginDisposition(concierge, id, action)
		if err != nil {
			return err
		}
		candidate := ReplyResult{TaskID: reply.Task, State: state}
		if disposition.State == inbox.DispositionComplete {
			if disposition.Outcome != string(state) {
				return inbox.ErrDispositionConflict
			}
			candidate.Already = true
		} else {
			candidate.Already = resultAlreadyApplied(current, generation, mode)
			if current.HasReceipt(generation, mode) {
				if err := taskview.Rebuild(ctx, service.root); err != nil {
					result = candidate
					return err
				}
			} else {
				updated, err := finish(task, generation, mode, closedOn)
				if err != nil {
					if updated.ID() != "" {
						result = candidate
					}
					return err
				}
			}
			result = candidate
			if _, err := messages.CompleteDisposition(concierge, id, string(state)); err != nil {
				return err
			}
		}
		result = candidate
		return messages.Acknowledge(concierge, id)
	})
	return result, err
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
