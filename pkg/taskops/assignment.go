package taskops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

// Notification describes the wake outcome independently of durable delivery.
type Notification string

const (
	NotificationSent         Notification = "sent"
	NotificationAcknowledged Notification = "acknowledged"
	NotificationFailed       Notification = "failed"
)

// AssignmentResult contains a message only after its publication is confirmed.
type AssignmentResult struct {
	Message           inbox.Message
	Notification      Notification
	NotificationError error
}

// SendAssignment binds durable work to a confirmed generation before waking it.
func (service *Service) SendAssignment(
	ctx context.Context, messageRoot, taskID, key, body string, client herdr.Client,
) (AssignmentResult, error) {
	if strings.TrimSpace(body) == "" {
		return AssignmentResult{}, inbox.ErrBlankBody
	}
	worker, err := service.SupervisedWorker(taskID, messageRoot, client)
	if err != nil {
		return AssignmentResult{}, err
	}
	return worker.sendAssignment(ctx, key, body, worker.messages.Send)
}

func (worker *SupervisedWorker) sendAssignment(
	ctx context.Context, key, body string,
	send func(inbox.Recipient, string, string) (inbox.Message, error),
) (AssignmentResult, error) {
	service, taskID := worker.service, worker.taskID
	var result AssignmentResult
	_, err := service.store.WithTask(ctx, taskID, func(task *taskstate.LockedTask) error {
		bound, err := assignmentWorker(task.Current())
		if err != nil {
			return err
		}
		envelope, err := json.Marshal(taskprotocol.Assignment{
			Kind: "task-assignment", State: filepath.Join(service.root, "tasks"),
			Task: taskID, SpawnGeneration: bound.Generation, Body: body,
		})
		if err != nil {
			return err
		}
		if key != "" {
			key = fmt.Sprintf(
				"task-assignment:%s:%s:%s:%s",
				service.root,
				taskID,
				bound.Generation,
				key,
			)
		}
		message, err := send(worker.recipient, key, string(envelope))
		if err != nil {
			return err
		}
		result.Message = message
		result.Notification = NotificationFailed
		pending, err := worker.messages.List(worker.recipient)
		if err != nil {
			result.NotificationError = err
			return nil
		}
		if len(pending) == 0 {
			result.Notification = NotificationAcknowledged
			return nil
		}
		result.NotificationError = worker.promptBoundWorker(ctx, task)
		if result.NotificationError == nil {
			result.Notification = NotificationSent
		}
		return nil
	})
	return result, err
}

func assignmentWorker(task taskstate.Task) (taskstate.Worker, error) {
	snapshot := task.Snapshot()
	bound := snapshot.Worker
	if snapshot.State != taskstate.InFlight || snapshot.Completion != nil || bound == nil ||
		bound.PendingAdoption || bound.PendingGeneration != "" || bound.PendingLaunch != "" {
		return taskstate.Worker{}, taskstate.ErrInvalidTransition
	}
	return *bound, nil
}

func (worker *SupervisedWorker) promptBoundWorker(
	ctx context.Context,
	task *taskstate.LockedTask,
) error {
	client, agent, err := worker.notificationEndpoint(ctx, task)
	if err != nil {
		return err
	}
	return client.Prompt(ctx, agent.PaneID, notify.DoorbellText)
}
