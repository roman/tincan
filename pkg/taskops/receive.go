package taskops

import (
	"context"
	"errors"
	"os"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

// AssignmentState indicates whether a worker can act on an assignment.
type AssignmentState string

const (
	AssignmentCurrent      AssignmentState = "current"
	AssignmentStale        AssignmentState = "stale"
	AssignmentAcknowledged AssignmentState = "acknowledged"
)

// AssignmentStatus includes an actionable body only for a current read.
type AssignmentStatus struct {
	State AssignmentState
	Body  string
}

type assignmentOperation int

const (
	readAssignment assignmentOperation = iota
	acknowledgeAssignment
)

// ReadAssignment returns work only for the caller's confirmed generation.
func (service *Service) ReadAssignment(
	ctx context.Context, messageRoot, taskID, messageID, generation string,
) (AssignmentStatus, error) {
	return service.receiveAssignment(
		ctx,
		messageRoot,
		taskID,
		messageID,
		generation,
		readAssignment,
	)
}

// AcknowledgeAssignment retains the envelope for later bound replies.
func (service *Service) AcknowledgeAssignment(
	ctx context.Context, messageRoot, taskID, messageID, generation string,
) (AssignmentStatus, error) {
	return service.receiveAssignment(
		ctx,
		messageRoot,
		taskID,
		messageID,
		generation,
		acknowledgeAssignment,
	)
}

func (service *Service) receiveAssignment(
	ctx context.Context,
	messageRoot, taskID, messageID, generation string,
	operation assignmentOperation,
) (AssignmentStatus, error) {
	if err := firstmate.ValidateSpawnGeneration(generation); err != nil {
		return AssignmentStatus{}, err
	}
	id, err := inbox.ParseMessageID(messageID)
	if err != nil {
		return AssignmentStatus{}, err
	}
	recipient, err := inbox.ParseRecipient(taskID)
	if err != nil {
		return AssignmentStatus{}, err
	}
	messages, err := inbox.New(messageRoot)
	if err != nil {
		return AssignmentStatus{}, err
	}
	var result AssignmentStatus
	_, err = service.store.WithTask(ctx, taskID, func(task *taskstate.LockedTask) error {
		bound, err := assignmentWorker(task.Current())
		if err != nil {
			return err
		}
		if bound.Generation != generation {
			result.State = AssignmentStale
			return nil
		}
		message, err := messages.Read(recipient, id)
		handled := false
		if operation == acknowledgeAssignment && errors.Is(err, os.ErrNotExist) {
			message, err = messages.ReadHandled(recipient, id)
			handled = true
		}
		if err != nil {
			return err
		}
		assignment, err := taskprotocol.ReadAssignment(messages, service.root, taskID, message.ID)
		if err != nil {
			return err
		}
		if operation == acknowledgeAssignment {
			if err := messages.Acknowledge(recipient, id); err != nil {
				return err
			}
			if err := clearHandledPromptProgress(task, messageID); err != nil {
				return err
			}
			if handled || assignment.SpawnGeneration == generation {
				result.State = AssignmentAcknowledged
			} else {
				result.State = AssignmentStale
			}
			return nil
		}
		if assignment.SpawnGeneration != generation {
			result.State = AssignmentStale
			return nil
		}
		result = AssignmentStatus{State: AssignmentCurrent, Body: assignmentDisplay(assignment)}
		return nil
	})
	return result, err
}

func clearHandledPromptProgress(task *taskstate.LockedTask, assignmentID string) error {
	bound := task.Current().Snapshot().Worker
	if bound == nil || bound.PromptProgress == nil ||
		bound.PromptProgress.AssignmentID != assignmentID {
		return nil
	}
	_, err := task.Apply(
		func(current taskstate.Task) (taskstate.Task, error) { return current.ClearPromptProgress(assignmentID) },
	)
	return err
}
