package taskstate

import "github.com/roman/tincan/pkg/firstmate"

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
	if worker.PendingLaunch == worker.Generation {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.Worker.PendingLaunch = ""
	return newTransition(snapshot)
}
