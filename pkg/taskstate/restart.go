package taskstate

import (
	"github.com/roman/tincan/pkg/firstmate"
	"slices"
)

// ReserveLaunch keeps one new generation through restart retries.
func (task Task) ReserveLaunch(candidate string) (Task, error) {
	worker := task.record.Worker
	if task.record.LaunchIntent != nil || task.State() != InFlight || worker == nil ||
		worker.PendingAdoption ||
		worker.PendingGeneration != "" ||
		task.record.Completion != nil ||
		firstmate.ValidateSpawnGeneration(candidate) != nil {
		return Task{}, ErrInvalidTransition
	}
	if worker.PendingLaunch != "" {
		return task, nil
	}
	for _, plan := range worker.Restarts {
		if candidate == plan.From || candidate == plan.To {
			return Task{}, ErrInvalidTransition
		}
	}
	if candidate == worker.Generation {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.Worker.PendingLaunch = candidate
	return newTransition(snapshot)
}

// AdvanceGeneration records the reserved incarnation before worker launch.
func (task Task) AdvanceGeneration(next string) (Task, error) {
	worker := task.record.Worker
	if task.State() != InFlight || worker == nil || worker.PendingAdoption ||
		task.record.Completion != nil {
		return Task{}, ErrInvalidTransition
	}
	if worker.PendingGeneration != "" {
		if worker.PendingGeneration != next ||
			(worker.PendingLaunch != "" && worker.PendingLaunch != next) {
			return Task{}, ErrInvalidTransition
		}
		snapshot := task.Snapshot()
		snapshot.Worker.PromptProgress = nil
		snapshot.Worker.Generation = next
		snapshot.Worker.PendingGeneration = ""
		if snapshot.Worker.PendingLaunch == "" {
			snapshot.Worker.PendingLaunch = next
		}
		return newTransition(snapshot)
	}
	if worker.PendingLaunch == "" || worker.PendingLaunch != next {
		return Task{}, ErrInvalidTransition
	}
	if worker.Generation == next {
		return task, nil
	}
	snapshot := task.Snapshot()
	snapshot.Worker.PromptProgress = nil
	snapshot.Worker.Generation = next
	return newTransition(snapshot)
}

// CompleteLaunch clears the reservation after the new worker is live.
func (task Task) CompleteLaunch() (Task, error) {
	worker := task.record.Worker
	if task.State() != InFlight || worker == nil || worker.PendingAdoption ||
		worker.PendingGeneration != "" || task.record.Completion != nil {
		return Task{}, ErrInvalidTransition
	}
	if worker.PendingLaunch == "" {
		return task, nil
	}
	if worker.PendingLaunch != worker.Generation {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.Worker.PendingLaunch = ""
	return newTransition(snapshot)
}

// CancelLaunch clears a reservation when the previous worker remains live.
func (task Task) CancelLaunch() (Task, error) {
	worker := task.record.Worker
	if task.State() != InFlight || worker == nil || worker.PendingAdoption ||
		worker.PendingGeneration != "" || task.record.Completion != nil {
		return Task{}, ErrInvalidTransition
	}
	if worker.PendingLaunch == "" {
		return task, nil
	}
	if worker.PendingLaunch == worker.Generation ||
		(len(worker.Restarts) > 0 && worker.Restarts[len(worker.Restarts)-1].To == worker.PendingLaunch) {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.Worker.PendingLaunch = ""
	return newTransition(snapshot)
}

// RecordRestart freezes recovery before publication or generation advancement.
func (task Task) RecordRestart(plan RestartPlan) (Task, error) {
	snapshot := task.Snapshot()
	worker := snapshot.Worker
	if worker == nil || snapshot.State != InFlight || worker.PendingLaunch != plan.To ||
		snapshot.Completion != nil {
		return Task{}, ErrInvalidTransition
	}
	if len(worker.Restarts) > 0 && worker.Restarts[len(worker.Restarts)-1].To == plan.To {
		existing := worker.Restarts[len(worker.Restarts)-1]
		if existing.From != plan.From || !slices.Equal(existing.Assignments, plan.Assignments) ||
			!slices.Equal(existing.Replies, plan.Replies) {
			return Task{}, ErrInvalidTransition
		}
		return task, nil
	}
	if plan.From == plan.To || plan.From != worker.Generation {
		return Task{}, ErrInvalidTransition
	}
	worker.Restarts = append(worker.Restarts, plan)
	return newTransition(snapshot)
}
