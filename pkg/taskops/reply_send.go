package taskops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

// ReplyInput identifies one worker result and its optional assignment.
type ReplyInput struct {
	TaskID       string
	Generation   string
	Key          string
	AssignmentID string
	Body         string
}

// ReplyAcknowledgement describes assignment handling after reply publication.
type ReplyAcknowledgement string

const (
	ReplyUnbound                    ReplyAcknowledgement = "not_requested"
	ReplyAcknowledgementUnconfirmed ReplyAcknowledgement = "unconfirmed"
	ReplyAcknowledged               ReplyAcknowledgement = "acknowledged"
)

// ReplyDelivery preserves confirmed delivery when assignment handling fails.
type ReplyDelivery struct {
	Message    inbox.Message
	Assignment ReplyAcknowledgement
}

// SendReply publishes current work or confirms an exact existing reply on retry.
func (service *Service) SendReply(
	ctx context.Context, messageRoot string, input ReplyInput,
) (ReplyDelivery, error) {
	if err := firstmate.ValidateSpawnGeneration(input.Generation); err != nil {
		return ReplyDelivery{}, err
	}
	if strings.TrimSpace(input.Key) == "" {
		return ReplyDelivery{}, inbox.ErrMissingKey
	}
	if strings.TrimSpace(input.Body) == "" {
		return ReplyDelivery{}, inbox.ErrBlankBody
	}
	worker, err := inbox.ParseRecipient(input.TaskID)
	if err != nil {
		return ReplyDelivery{}, err
	}
	var assignmentID inbox.MessageID
	if input.AssignmentID != "" {
		assignmentID, err = inbox.ParseMessageID(input.AssignmentID)
		if err != nil {
			return ReplyDelivery{}, err
		}
	}
	messages, err := inbox.New(messageRoot)
	if err != nil {
		return ReplyDelivery{}, err
	}
	var result ReplyDelivery
	_, err = service.store.WithTask(ctx, input.TaskID, func(task *taskstate.LockedTask) error {
		if input.AssignmentID != "" {
			if err := taskprotocol.ValidateBoundAssignment(messages, service.root, input.TaskID, input.Generation, assignmentID); err != nil {
				return err
			}
		}
		replyKey, suffix := "", input.Key
		if input.AssignmentID != "" {
			replyKey, suffix = input.Key, input.AssignmentID
		}
		envelope, err := json.Marshal(taskprotocol.Reply{
			Kind: "task-reply", State: filepath.Join(service.root, "tasks"),
			Task: input.TaskID, SpawnGeneration: input.Generation,
			AssignmentID: input.AssignmentID, ReplyKey: replyKey, Body: input.Body,
		})
		if err != nil {
			return err
		}
		key := fmt.Sprintf(
			"task-reply:%s:%s:%s:%s",
			service.root,
			input.TaskID,
			input.Generation,
			suffix,
		)
		publish := messages.ConfirmExisting
		bound, bindingErr := assignmentWorker(task.Current())
		if bindingErr == nil && bound.Generation == input.Generation {
			publish = messages.Send
		}
		message, err := publish(service.primaryRecipient, key, string(envelope))
		if err != nil {
			return err
		}
		result = ReplyDelivery{Message: message, Assignment: ReplyUnbound}
		if input.AssignmentID == "" {
			return nil
		}
		result.Assignment = ReplyAcknowledgementUnconfirmed
		if err := clearHandledPromptProgress(task, input.AssignmentID); err != nil {
			return err
		}
		if err := messages.Acknowledge(worker, assignmentID); err != nil {
			return err
		}
		result.Assignment = ReplyAcknowledged
		return nil
	})
	return result, err
}
