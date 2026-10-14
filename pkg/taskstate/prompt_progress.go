package taskstate

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/inbox"
)

const MaximumPromptAttempts = 3

// PromptProgress preserves one unfinished assignment's notification budget.
type PromptProgress struct {
	AssignmentID  string      `json:"assignment_id"`
	StartedAt     time.Time   `json:"started_at"`
	LastAttemptAt time.Time   `json:"last_attempt_at"`
	Attempts      int         `json:"attempts"`
	Alert         *StallAlert `json:"alert,omitempty"`
}

// StallAlert freezes diagnostic evidence before its durable publication.
type StallAlert struct {
	ObservedAt  time.Time        `json:"observed_at"`
	State       herdr.AgentState `json:"state"`
	Screen      string           `json:"screen"`
	ScreenError string           `json:"screen_error,omitempty"`
}

func clonePromptProgress(input *PromptProgress) *PromptProgress {
	if input == nil {
		return nil
	}
	result := *input
	if input.Alert != nil {
		alert := *input.Alert
		result.Alert = &alert
	}
	return &result
}

func validPromptProgress(progress *PromptProgress) bool {
	if progress == nil {
		return true
	}
	if _, err := inbox.ParseMessageID(progress.AssignmentID); err != nil {
		return false
	}
	if progress.StartedAt.IsZero() ||
		((progress.Attempts == 0 && !progress.LastAttemptAt.IsZero()) || (progress.Attempts > 0 && progress.LastAttemptAt.Before(progress.StartedAt))) ||
		progress.Attempts < 0 ||
		progress.Attempts > MaximumPromptAttempts {
		return false
	}
	if alert := progress.Alert; alert != nil {
		if alert.ObservedAt.Before(progress.StartedAt) ||
			alert.ObservedAt.Before(progress.LastAttemptAt) ||
			!slices.Contains(
				[]herdr.AgentState{herdr.Idle, herdr.Done, herdr.Blocked, herdr.Unknown},
				alert.State,
			) ||
			!utf8.ValidString(alert.Screen) ||
			len(alert.Screen) > 4096 ||
			strings.ContainsRune(alert.Screen, 0) ||
			!utf8.ValidString(alert.ScreenError) ||
			len(alert.ScreenError) > 4096 ||
			strings.ContainsRune(alert.ScreenError, 0) {
			return false
		}
	}
	return true
}

func (task Task) promptSnapshot() (Snapshot, error) {
	snapshot := task.Snapshot()
	worker := snapshot.Worker
	if snapshot.State != InFlight || snapshot.Completion != nil || worker == nil ||
		worker.PendingAdoption ||
		worker.PendingLaunch != "" ||
		worker.PendingGeneration != "" {
		return Snapshot{}, ErrInvalidTransition
	}
	return snapshot, nil
}

// BeginPromptProgress retains the assignment publication time across host restarts.
func (task Task) BeginPromptProgress(assignmentID string, createdAt time.Time) (Task, error) {
	snapshot, err := task.promptSnapshot()
	if err != nil {
		return Task{}, err
	}
	if progress := snapshot.Worker.PromptProgress; progress != nil {
		if progress.AssignmentID != assignmentID || !progress.StartedAt.Equal(createdAt) {
			return Task{}, ErrInvalidTransition
		}
		return task, nil
	}
	snapshot.Worker.PromptProgress = &PromptProgress{
		AssignmentID: assignmentID,
		StartedAt:    createdAt,
	}
	return newTransition(snapshot)
}

// ReservePromptAttempt counts one potentially uncertain notification before sending.
func (task Task) ReservePromptAttempt(assignmentID string, at time.Time) (Task, error) {
	snapshot, err := task.promptSnapshot()
	if err != nil {
		return Task{}, err
	}
	progress := snapshot.Worker.PromptProgress
	if progress == nil || progress.AssignmentID != assignmentID ||
		progress.Attempts >= MaximumPromptAttempts ||
		progress.Alert != nil ||
		at.Before(progress.StartedAt) ||
		(progress.Attempts > 0 && at.Before(progress.LastAttemptAt)) {
		return Task{}, ErrInvalidTransition
	}
	progress.Attempts++
	progress.LastAttemptAt = at
	return newTransition(snapshot)
}

// FreezeStallAlert retains the first diagnostic snapshot across publication retries.
func (task Task) FreezeStallAlert(assignmentID string, alert StallAlert) (Task, error) {
	snapshot, err := task.promptSnapshot()
	if err != nil {
		return Task{}, err
	}
	progress := snapshot.Worker.PromptProgress
	if progress == nil || progress.AssignmentID != assignmentID {
		return Task{}, ErrInvalidTransition
	}
	if progress.Alert != nil {
		if *progress.Alert != alert {
			return Task{}, ErrInvalidTransition
		}
		return task, nil
	}
	progress.Alert = &alert
	return newTransition(snapshot)
}

// ClearPromptProgress requires the matching assignment before releasing its budget.
func (task Task) ClearPromptProgress(assignmentID string) (Task, error) {
	snapshot := task.Snapshot()
	if snapshot.Worker == nil {
		return Task{}, ErrInvalidTransition
	}
	progress := snapshot.Worker.PromptProgress
	if progress == nil {
		return task, nil
	}
	if progress.AssignmentID != assignmentID {
		return Task{}, ErrInvalidTransition
	}
	snapshot.Worker.PromptProgress = nil
	return newTransition(snapshot)
}
