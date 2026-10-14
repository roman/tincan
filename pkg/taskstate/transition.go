package taskstate

import (
	"errors"
	"fmt"
	"reflect"
)

var ErrInvalidTransition = errors.New("invalid task transition")

// ReserveAdoption records launch identity before a worker is fully bound.
func (task Task) ReserveAdoption(worker Worker) (Task, error) {
	if task.State() != Queued || task.record.Completion != nil ||
		worker.Workspace != "" || worker.Tab != "" || worker.PendingAdoption ||
		worker.PendingGeneration != "" || worker.PendingLaunch != "" {
		return Task{}, ErrInvalidTransition
	}
	worker.PendingAdoption = true
	if task.record.Worker != nil {
		if reflect.DeepEqual(*task.record.Worker, worker) {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.Worker = &worker
	return newTransition(snapshot)
}

// ConfirmAdoption binds the reserved worker to its Herdr workspace and tab.
func (task Task) ConfirmAdoption(workspace, tab string) (Task, error) {
	worker := task.record.Worker
	if worker == nil || task.State() != Queued || task.record.Completion != nil {
		return Task{}, ErrInvalidTransition
	}
	if !worker.PendingAdoption {
		if worker.Workspace == workspace && worker.Tab == tab {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	if (worker.Workspace != "" && worker.Workspace != workspace) ||
		(worker.Tab != "" && worker.Tab != tab) {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.Worker.Workspace = workspace
	snapshot.Worker.Tab = tab
	snapshot.Worker.PendingAdoption = false
	return newTransition(snapshot)
}

// CancelAdoption releases a reservation that has no confirmed worker binding.
func (task Task) CancelAdoption() (Task, error) {
	if task.State() != Queued || task.record.LaunchIntent != nil {
		return Task{}, ErrInvalidTransition
	}
	if task.record.Worker == nil {
		if task.record.Receipt != nil || task.record.Completion != nil {
			return Task{}, ErrInvalidTransition
		}
		return task, nil
	}
	if !task.record.Worker.PendingAdoption || task.record.Worker.Workspace != "" ||
		task.record.Worker.Tab != "" {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.Worker = nil
	return newTransition(snapshot)
}

// PlaceHold prevents a queued task from starting or an in-flight task from closing.
func (task Task) PlaceHold(hold Hold) (Task, error) {
	if task.State() == Done || task.record.Completion != nil {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.Definition.Hold = &hold
	return newTransition(snapshot)
}

// ReleaseHold removes a task hold before dispatch or close.
func (task Task) ReleaseHold() (Task, error) {
	if task.State() == Done || task.record.Completion != nil {
		return Task{}, ErrInvalidTransition
	}
	if task.record.Hold == nil {
		return task, nil
	}
	snapshot := task.Snapshot()
	snapshot.Definition.Hold = nil
	return newTransition(snapshot)
}

// Start moves a confirmed, unheld local task into flight.
// The caller must check dependency records before this transition.
func (task Task) Start() (Task, error) {
	if task.State() == InFlight && task.record.Completion == nil && task.record.Worker != nil {
		return task, nil
	}
	worker := task.record.Worker
	if task.State() != Queued || worker == nil || worker.PendingAdoption ||
		worker.PendingGeneration != "" || worker.PendingLaunch != "" ||
		task.record.Completion != nil || task.record.Hold != nil {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.State = InFlight
	return newTransition(snapshot)
}

// AcceptResult records one worker result and its queue transition atomically.
func (task Task) AcceptResult(
	generation string,
	mode CompletionMode,
	closedOn string,
) (Task, error) {
	if task.record.LaunchIntent != nil {
		return Task{}, ErrInvalidTransition
	}
	if (mode == Retain && closedOn != "") ||
		(mode == Close && (closedOn == "" ||
			(task.State() == Done && task.record.Closed != closedOn))) {
		return Task{}, ErrInvalidTransition
	}
	if task.record.Completion == nil && task.record.Worker == nil && task.record.Receipt != nil {
		receipt := task.record.Receipt
		if receipt.Generation == generation && receipt.Mode == mode &&
			((mode == Close && task.State() == Done) ||
				(mode == Retain && task.State() == Queued)) {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	if task.record.Completion != nil {
		completion := task.record.Completion
		if completion.Generation != generation || completion.Mode != mode {
			return Task{}, ErrInvalidTransition
		}
		if (mode == Close && task.State() == Done) ||
			(mode == Retain && task.State() == Queued) {
			return task, nil
		}
		if task.State() != InFlight {
			return Task{}, ErrInvalidTransition
		}
	}
	if task.record.Completion == nil {
		worker := task.record.Worker
		if task.State() != InFlight || worker == nil || worker.Generation != generation ||
			worker.PendingAdoption || worker.PendingGeneration != "" || worker.PendingLaunch != "" {
			return Task{}, ErrInvalidTransition
		}
	}
	snapshot := task.Snapshot()
	switch mode {
	case Close:
		if task.record.Hold != nil {
			return Task{}, ErrInvalidTransition
		}
		snapshot.State = Done
		snapshot.Definition.Closed = closedOn
	case Retain:
		if task.record.Hold == nil {
			return Task{}, ErrInvalidTransition
		}
		snapshot.State = Queued
	default:
		return Task{}, ErrInvalidTransition
	}
	if snapshot.Completion == nil {
		snapshot.Completion = &Completion{Generation: generation, Mode: mode, Phase: Accepted}
	}
	return newTransition(snapshot)
}

// RetireResult clears a departed worker and keeps its completion receipt.
func (task Task) RetireResult(generation string) (Task, error) {
	if task.record.Completion == nil {
		if task.HasReceipt(generation, Close) || task.HasReceipt(generation, Retain) {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	completion := task.record.Completion
	if completion.Generation != generation {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	if snapshot.Receipt != nil && snapshot.Receipt.Generation != generation {
		snapshot.PriorReceipts = append(snapshot.PriorReceipts, *snapshot.Receipt)
	}
	snapshot.Receipt = &Receipt{Generation: generation, Mode: completion.Mode}
	if snapshot.Worker != nil {
		snapshot.Receipt.Lease = snapshot.Worker.Lease
	}
	snapshot.Completion = nil
	snapshot.Worker = nil
	return newTransition(snapshot)
}

func newTransition(snapshot Snapshot) (Task, error) {
	updated, err := NewMigrated(snapshot)
	if err != nil {
		return Task{}, fmt.Errorf("%w: %v", ErrInvalidTransition, err)
	}
	return updated, nil
}
