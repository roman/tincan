package hostservice

import (
	"context"
	"errors"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskops"
)

const SendAssignment Operation = "send_assignment"

type AssignmentRequest struct {
	TaskID string `json:"task_id"`
	Key    string `json:"key,omitempty"`
	Body   string `json:"body"`
}

type AssignmentResult struct {
	TaskID            string               `json:"task_id"`
	MessageID         string               `json:"message_id"`
	Notification      taskops.Notification `json:"notification"`
	NotificationError string               `json:"notification_error,omitempty"`
}

func validateAssignmentInput(input *AssignmentRequest) error {
	if input == nil {
		return errors.New("send requires an assignment")
	}
	if _, err := firstmate.ParseTaskID(input.TaskID); err != nil {
		return err
	}
	if strings.TrimSpace(input.Body) == "" {
		return inbox.ErrBlankBody
	}
	return nil
}

func (server *Server) sendAssignment(
	ctx context.Context,
	input AssignmentRequest,
) (Response, error) {
	result, err := server.service.SendAssignment(
		ctx,
		server.config.MessageRoot,
		input.TaskID,
		input.Key,
		input.Body,
		herdr.NewClient(server.config.HerdrExecutable),
	)
	response := Response{}
	if result.Message.ID.String() != "" {
		response.Assignment = &AssignmentResult{
			TaskID:       input.TaskID,
			MessageID:    result.Message.ID.String(),
			Notification: result.Notification,
		}
		if result.NotificationError != nil {
			response.Assignment.NotificationError = result.NotificationError.Error()
		}
	}
	return response, err
}

func validateAssignmentResponse(response Response, request Request) error {
	if err := validateAssignmentInput(request.Assignment); err != nil {
		return err
	}
	result := response.Assignment
	if result == nil || response.Task != nil || response.Transition != nil ||
		response.Resolution != nil ||
		response.Stopped != nil ||
		response.Error != "" {
		return errors.New("host returned an incomplete assignment result")
	}
	if result.TaskID != request.Assignment.TaskID {
		return errors.New("host returned a different assignment task")
	}
	if _, err := inbox.ParseMessageID(result.MessageID); err != nil {
		return err
	}
	switch result.Notification {
	case taskops.NotificationFailed:
		if result.NotificationError == "" {
			return errors.New("failed notification requires an error")
		}
	case taskops.NotificationSent, taskops.NotificationAcknowledged:
		if result.NotificationError != "" {
			return errors.New("successful notification forbids an error")
		}
	default:
		return errors.New("host returned an unknown notification result")
	}
	return nil
}
