package taskops

import (
	"context"
	"errors"

	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
)

// ReserveAdoption records a worker before its live pane is fully bound.
func (service *Service) ReserveAdoption(
	ctx context.Context, taskID string, worker taskstate.Worker,
) (taskstate.Task, error) {
	updated, err := service.store.ReserveAdoption(ctx, taskID, worker)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}

// ConfirmAdoption binds the reserved worker to its verified Herdr location.
func (service *Service) ConfirmAdoption(
	ctx context.Context, taskID, workspace, tab string,
) (taskstate.Task, error) {
	return service.updateAdoption(ctx, taskID, func(task taskstate.Task) (taskstate.Task, error) {
		return task.ConfirmAdoption(workspace, tab)
	})
}

// CancelAdoption releases a reservation that has no confirmed worker binding.
func (service *Service) CancelAdoption(
	ctx context.Context, taskID string,
) (taskstate.Task, error) {
	return service.updateAdoption(ctx, taskID, taskstate.Task.CancelAdoption)
}

func (service *Service) updateAdoption(
	ctx context.Context, taskID string,
	transition func(taskstate.Task) (taskstate.Task, error),
) (taskstate.Task, error) {
	updated, err := service.store.Update(ctx, taskID, transition)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}
