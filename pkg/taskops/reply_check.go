package taskops

import (
	"context"
	"errors"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

// ReplyState indicates whether a pending reply matches a confirmed worker.
type ReplyState string

const (
	ReplyCurrent ReplyState = "current"
	ReplyStale   ReplyState = "stale"
)

// CheckedReply includes actionable content only after every binding check.
type CheckedReply struct {
	State        ReplyState
	TaskID       string
	Generation   string
	AssignmentID string
	Body         string
}

// CheckReply reads a snapshot without acknowledging or accepting the result.
// Resolve revalidates the reply before changing the task.
func (service *Service) CheckReply(
	ctx context.Context, messageRoot, messageID string,
) (CheckedReply, error) {
	id, err := inbox.ParseMessageID(messageID)
	if err != nil {
		return CheckedReply{}, err
	}
	messages, err := inbox.New(messageRoot)
	if err != nil {
		return CheckedReply{}, err
	}
	message, err := messages.Read(service.primaryRecipient, id)
	if err != nil {
		return CheckedReply{}, err
	}
	reply, err := taskprotocol.DecodeReply(message.Body, message.Key, service.root)
	if err != nil {
		return CheckedReply{}, err
	}
	stale := CheckedReply{State: ReplyStale, TaskID: reply.Task}
	task, err := service.store.Read(ctx, reply.Task)
	if errors.Is(err, taskstate.ErrTaskNotFound) {
		return stale, nil
	}
	if err != nil {
		return CheckedReply{}, err
	}
	worker, err := assignmentWorker(task)
	if err != nil ||
		(worker.Generation != reply.SpawnGeneration && !preservedReply(worker, messageID)) {
		return stale, nil
	}
	if reply.AssignmentID != "" {
		assignmentID, _ := inbox.ParseMessageID(reply.AssignmentID)
		if err := taskprotocol.ValidateBoundAssignment(messages, service.root, reply.Task, reply.SpawnGeneration, assignmentID); err != nil {
			return CheckedReply{}, err
		}
	}
	return CheckedReply{
		State: ReplyCurrent, TaskID: reply.Task, Generation: worker.Generation,
		AssignmentID: reply.AssignmentID, Body: reply.Body,
	}, nil
}
