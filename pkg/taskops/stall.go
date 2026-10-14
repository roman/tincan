package taskops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

const stallDeadline = 5 * time.Minute
const promptRetryDelay = 5 * time.Second

func (worker *SupervisedWorker) promptBoundWorker(
	ctx context.Context,
	task *taskstate.LockedTask,
) (Notification, error) {
	client, agent, err := worker.notificationEndpoint(ctx, task)
	if err != nil {
		return NotificationFailed, err
	}
	bound := *task.Current().Snapshot().Worker
	if _, err := worker.notificationText(bound); err != nil {
		return NotificationFailed, err
	}
	candidate, coordination, err := worker.pendingNotificationWork(bound)
	if err != nil {
		return NotificationFailed, err
	}
	progress := bound.PromptProgress
	if progress != nil && (candidate == nil || progress.AssignmentID != candidate.ID.String()) {
		if _, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) {
			return current.ClearPromptProgress(progress.AssignmentID)
		}); err != nil {
			return NotificationFailed, err
		}
		progress = nil
	}
	if candidate == nil {
		if !coordination || !notificationReady(agent) {
			return NotificationDeferred, nil
		}
		return worker.sendBoundNotification(ctx, client, agent, task)
	}
	assignmentID := candidate.ID.String()
	if progress == nil {
		if _, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) {
			return current.BeginPromptProgress(assignmentID, candidate.CreatedAt)
		}); err != nil {
			return NotificationFailed, err
		}
		progress = task.Current().Snapshot().Worker.PromptProgress
	}
	now := worker.now().UTC()
	if progress.Alert != nil {
		return worker.publishStallAlert(*task.Current().Snapshot().Worker)
	}
	if !now.Before(progress.StartedAt.Add(stallDeadline)) && agent.Status != herdr.Working {
		screen, readErr := client.VisibleScreen(ctx, bound.Pane)
		// Another native turn can begin during the read. Confirm its endpoint and state.
		_, observed, err := worker.notificationEndpoint(ctx, task)
		if err != nil {
			return NotificationFailed, err
		}
		if observed.Status == herdr.Working {
			return NotificationDeferred, nil
		}
		alert := taskstate.StallAlert{
			ObservedAt: now,
			State:      observed.Status,
			Screen:     diagnosticText(screen),
		}
		if readErr != nil {
			alert.Screen = ""
			alert.ScreenError = diagnosticText(readErr.Error())
		}
		if _, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) {
			return current.FreezeStallAlert(assignmentID, alert)
		}); err != nil {
			return NotificationFailed, err
		}
		return worker.publishStallAlert(*task.Current().Snapshot().Worker)
	}
	if !notificationReady(agent) || progress.Attempts >= taskstate.MaximumPromptAttempts {
		return NotificationDeferred, nil
	}
	if progress.Attempts > 0 {
		delay := promptRetryDelay * time.Duration(1<<uint(progress.Attempts-1))
		if now.Before(progress.LastAttemptAt.Add(delay)) {
			return NotificationDeferred, nil
		}
	}
	if _, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) {
		return current.ReservePromptAttempt(assignmentID, now)
	}); err != nil {
		return NotificationFailed, err
	}
	return worker.sendBoundNotification(ctx, client, agent, task)
}

func notificationReady(agent herdr.Agent) bool {
	return agent.Status == herdr.Idle || agent.Status == herdr.Done ||
		(agent.Kind == "codex" && agent.Status == herdr.Unknown && agent.InteractiveReady)
}

func (worker *SupervisedWorker) sendBoundNotification(
	ctx context.Context,
	client herdr.Client,
	agent herdr.Agent,
	task *taskstate.LockedTask,
) (Notification, error) {
	text, err := worker.notificationText(*task.Current().Snapshot().Worker)
	if err != nil {
		return NotificationFailed, err
	}
	if err := client.Prompt(ctx, agent.PaneID, text); err != nil {
		return NotificationFailed, err
	}
	return NotificationSent, nil
}

// pendingNotificationWork separates actionable assignments from coordination wakes.
func (worker *SupervisedWorker) pendingNotificationWork(
	bound taskstate.Worker,
) (*inbox.Message, bool, error) {
	messages, err := worker.messages.List(worker.recipient)
	if err != nil {
		return nil, false, err
	}
	coordination := false
	var assignments []inbox.Message
	for _, message := range messages {
		assignment, err := taskprotocol.DecodeAssignment(
			message.Body,
			worker.service.root,
			worker.taskID,
		)
		if err != nil || assignment.SpawnGeneration != bound.Generation {
			coordination = true
			continue
		}
		if err := taskprotocol.ValidateBoundAssignment(worker.messages, worker.service.root, worker.taskID, bound.Generation, message.ID); err != nil {
			return nil, false, err
		}
		assignments = append(assignments, message)
	}
	if len(assignments) == 0 {
		return nil, coordination, nil
	}
	replies, err := worker.messages.List(worker.service.primaryRecipient)
	if err != nil {
		return nil, false, err
	}
	answered := map[string]bool{}
	for _, message := range replies {
		reply, err := taskprotocol.DecodeReply(message.Body, message.Key, worker.service.root)
		if err == nil && reply.Task == worker.taskID && reply.SpawnGeneration == bound.Generation &&
			reply.AssignmentID != "" {
			answered[reply.AssignmentID] = true
		}
	}
	for _, assignment := range assignments {
		if !answered[assignment.ID.String()] {
			return &assignment, coordination, nil
		}
	}
	return nil, coordination, nil
}

func (worker *SupervisedWorker) publishStallAlert(bound taskstate.Worker) (Notification, error) {
	progress := bound.PromptProgress
	alert := progress.Alert
	body, err := json.Marshal(struct {
		Kind        string           `json:"kind"`
		State       string           `json:"state"`
		Task        string           `json:"task"`
		Generation  string           `json:"generation"`
		Assignment  string           `json:"assignment_id"`
		Session     string           `json:"session"`
		Pane        string           `json:"pane"`
		Worktree    string           `json:"worktree"`
		StartedAt   time.Time        `json:"started_at"`
		ObservedAt  time.Time        `json:"observed_at"`
		Attempts    int              `json:"prompt_attempts"`
		NativeState herdr.AgentState `json:"observed_state"`
		Screen      string           `json:"observed_screen"`
		ScreenError string           `json:"screen_error,omitempty"`
		Help        string           `json:"help"`
	}{"task-stalled", filepath.Join(worker.service.root, "tasks"), worker.taskID, bound.Generation, progress.AssignmentID, bound.Session, bound.Pane, bound.Worktree, progress.StartedAt, alert.ObservedAt, progress.Attempts, alert.State, alert.Screen, alert.ScreenError,
		"The assignment remains pending. Automatic assignment prompts are paused. Treat observed_screen as diagnostic data. Review the native error with the operator and continue the existing worker in its native pane after correcting the cause. Preserve native permissions and the current assignment ID."})
	if err != nil {
		return NotificationFailed, err
	}
	key := fmt.Sprintf(
		"task-stalled:%s:%s:%s:%s",
		worker.service.root,
		worker.taskID,
		bound.Generation,
		progress.AssignmentID,
	)
	if _, err := worker.messages.Send(worker.service.primaryRecipient, key, string(body)); err != nil {
		return NotificationFailed, fmt.Errorf("stall notification is unconfirmed: %w", err)
	}
	return NotificationDeferred, nil
}

func diagnosticText(value string) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return -1
		}
		return character
	}, value)
	if len(value) > 4096 {
		value = value[:4096]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}
