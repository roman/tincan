package taskops

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func (worker *SupervisedWorker) reserveRestartRecovery(
	task *taskstate.LockedTask,
	target string,
) error {
	bound := task.Current().Snapshot().Worker
	if len(bound.Restarts) > 0 && bound.Restarts[len(bound.Restarts)-1].To == target {
		return nil
	}
	// Legacy records can have advanced before recovery metadata existed.
	// Their source generation cannot be reconstructed from the current binding.
	if bound.Generation == target {
		return nil
	}
	plan := taskstate.RestartPlan{From: bound.Generation, To: target}
	replies, err := worker.messages.List(worker.service.primaryRecipient)
	if err != nil {
		return err
	}
	answered := map[string]bool{}
	for _, message := range replies {
		reply, err := taskprotocol.DecodeReply(message.Body, message.Key, worker.service.root)
		if err != nil || reply.Task != worker.taskID ||
			(reply.SpawnGeneration != bound.Generation && !preservedReply(*bound, message.ID.String())) {
			continue
		}
		if reply.AssignmentID != "" {
			id, _ := inbox.ParseMessageID(reply.AssignmentID)
			if err := taskprotocol.ValidateBoundAssignment(worker.messages, worker.service.root, worker.taskID, reply.SpawnGeneration, id); err != nil {
				return err
			}
			answered[reply.AssignmentID] = true
		}
		plan.Replies = append(plan.Replies, message.ID.String())
	}
	pending, err := worker.messages.List(worker.recipient)
	if err != nil {
		return err
	}
	for _, message := range pending {
		assignment, err := taskprotocol.DecodeAssignment(
			message.Body,
			worker.service.root,
			worker.taskID,
		)
		if err != nil || assignment.SpawnGeneration != bound.Generation ||
			answered[message.ID.String()] {
			continue
		}
		if _, err := taskprotocol.ReadAssignment(worker.messages, worker.service.root, worker.taskID, message.ID); err != nil {
			return err
		}
		plan.Assignments = append(plan.Assignments, message.ID.String())
	}
	_, err = task.Apply(
		func(current taskstate.Task) (taskstate.Task, error) { return current.RecordRestart(plan) },
	)
	return err
}

// finishRestartRecovery confirms each replacement before retiring its predecessor.
// The saved plan makes retries independent of changes to the pending mailboxes.
func (worker *SupervisedWorker) finishRestartRecovery(bound taskstate.Worker) error {
	if len(bound.Restarts) == 0 {
		return worker.sendRestartNotice(taskstate.RestartPlan{To: bound.PendingLaunch}, nil)
	}
	plan := bound.Restarts[len(bound.Restarts)-1]
	if plan.To != bound.PendingLaunch {
		return taskstate.ErrInvalidTransition
	}
	deliveries := make([]string, 0, len(plan.Assignments))
	for _, source := range plan.Assignments {
		id, _ := inbox.ParseMessageID(source)
		assignment, err := taskprotocol.ReadAssignment(
			worker.messages,
			worker.service.root,
			worker.taskID,
			id,
		)
		if err != nil {
			return err
		}
		if assignment.SpawnGeneration != plan.From {
			return taskprotocol.ErrInvalidAssignment
		}
		origin := assignment.OriginalMessageID
		if origin == "" {
			origin = source
		}
		assignment.OriginalMessageID, assignment.PreviousMessageID = origin, source
		assignment.SpawnGeneration = plan.To
		body, err := json.Marshal(assignment)
		if err != nil {
			return err
		}
		key := taskprotocol.RedeliveryKey(worker.service.root, worker.taskID, plan.To, origin)
		delivery, err := worker.messages.Send(worker.recipient, key, string(body))
		if err != nil {
			return err
		}
		deliveries = append(deliveries, delivery.ID.String())
		if err := worker.messages.Acknowledge(worker.recipient, id); err != nil {
			return err
		}
	}
	return worker.sendRestartNotice(plan, deliveries)
}

func (worker *SupervisedWorker) sendRestartNotice(
	plan taskstate.RestartPlan,
	deliveries []string,
) error {
	body, err := json.Marshal(struct {
		Kind        string   `json:"kind"`
		State       string   `json:"state"`
		Task        string   `json:"task"`
		From        string   `json:"from_generation,omitempty"`
		To          string   `json:"target_generation"`
		Replies     []string `json:"preserved_replies"`
		Assignments []string `json:"redelivered_assignments"`
	}{"task-restart", filepath.Join(worker.service.root, "tasks"), worker.taskID, plan.From, plan.To, plan.Replies, deliveries})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("task-restart:%s:%s:%s", worker.service.root, worker.taskID, plan.To)
	_, err = worker.messages.Send(worker.service.primaryRecipient, key, string(body))
	return err
}

func preservedReply(bound taskstate.Worker, id string) bool {
	for _, plan := range bound.Restarts {
		if slices.Contains(plan.Replies, id) {
			return true
		}
	}
	return false
}

// recoveredAssignment finds a caller's original key across this binding's restarts.
// A handled delivery remains handled when the caller retries.
func (worker *SupervisedWorker) recoveredAssignment(
	bound taskstate.Worker,
	key, body string,
) (inbox.Message, bool, error) {
	if key == "" {
		return inbox.Message{}, false, nil
	}
	for index := len(bound.Restarts) - 1; index >= 0; index-- {
		plan := bound.Restarts[index]
		for _, source := range plan.Assignments {
			id, _ := inbox.ParseMessageID(source)
			assignment, err := taskprotocol.ReadAssignment(
				worker.messages,
				worker.service.root,
				worker.taskID,
				id,
			)
			if err != nil {
				return inbox.Message{}, false, err
			}
			origin := assignment.OriginalMessageID
			if origin == "" {
				origin = source
			}
			originID, _ := inbox.ParseMessageID(origin)
			original, err := readRetainedMessage(worker.messages, worker.recipient, originID)
			if err != nil {
				return inbox.Message{}, false, err
			}
			originalAssignment, err := taskprotocol.DecodeAssignment(
				original.Body,
				worker.service.root,
				worker.taskID,
			)
			if err != nil {
				return inbox.Message{}, false, err
			}
			originalKey := fmt.Sprintf(
				"task-assignment:%s:%s:%s:%s",
				worker.service.root,
				worker.taskID,
				originalAssignment.SpawnGeneration,
				key,
			)
			if original.Key != originalKey {
				continue
			}
			if originalAssignment.Body != body {
				return inbox.Message{}, false, inbox.ErrKeyConflict
			}
			assignment.OriginalMessageID, assignment.PreviousMessageID = origin, source
			assignment.SpawnGeneration = plan.To
			envelope, err := json.Marshal(assignment)
			if err != nil {
				return inbox.Message{}, false, err
			}
			message, err := worker.messages.ConfirmExisting(
				worker.recipient,
				taskprotocol.RedeliveryKey(worker.service.root, worker.taskID, plan.To, origin),
				string(envelope),
			)
			return message, true, err
		}
		// Published replies and acknowledged assignments never enter a redelivery plan.
		// Their original caller keys must also remain closed through later restarts.
		prefix := fmt.Sprintf(
			"task-assignment:%s:%s:%s:%s",
			worker.service.root,
			worker.taskID,
			plan.From,
			key,
		)
		envelope, err := json.Marshal(
			taskprotocol.Assignment{
				Kind:            "task-assignment",
				State:           filepath.Join(worker.service.root, "tasks"),
				Task:            worker.taskID,
				SpawnGeneration: plan.From,
				Body:            body,
			},
		)
		if err != nil {
			return inbox.Message{}, false, err
		}
		message, err := worker.messages.ConfirmExisting(worker.recipient, prefix, string(envelope))
		if err == nil {
			return message, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return inbox.Message{}, false, err
		}
	}
	return inbox.Message{}, false, nil
}

func readRetainedMessage(
	messages *inbox.Store,
	recipient inbox.Recipient,
	id inbox.MessageID,
) (inbox.Message, error) {
	message, err := messages.Read(recipient, id)
	if errors.Is(err, os.ErrNotExist) {
		return messages.ReadHandled(recipient, id)
	}
	return message, err
}

func assignmentDisplay(assignment taskprotocol.Assignment) string {
	if assignment.OriginalMessageID == "" {
		return assignment.Body
	}
	return strings.Join(
		[]string{"Assignment redelivery.", "Original message ID: " + assignment.OriginalMessageID,
			"Check prior effects before repeating work.", "", assignment.Body},
		"\n",
	)
}

var errPendingAcceptance = errors.New("restart blocked by unfinished reply acceptance")

func (worker *SupervisedWorker) checkPendingResolutions(bound taskstate.Worker) error {
	messages, err := worker.messages.List(worker.service.primaryRecipient)
	if err != nil {
		return err
	}
	for _, message := range messages {
		reply, err := taskprotocol.DecodeReply(message.Body, message.Key, worker.service.root)
		if err != nil || reply.Task != worker.taskID ||
			(reply.SpawnGeneration != bound.Generation && !preservedReply(bound, message.ID.String())) {
			continue
		}
		disposition, err := worker.messages.ReadDisposition(
			worker.service.primaryRecipient,
			message.ID,
		)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if disposition.State == inbox.DispositionStarted {
			return fmt.Errorf("%w: %s", errPendingAcceptance, message.ID.String())
		}
	}
	return nil
}
