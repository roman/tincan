package taskops

import (
	"context"
	"errors"

	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
)

// PlaceHold replaces the task hold and rebuilds the backlog projection.
func (service *Service) PlaceHold(
	ctx context.Context,
	taskID string,
	hold taskstate.Hold,
) (taskstate.Task, error) {
	updated, err := service.store.Update(
		ctx,
		taskID,
		func(task taskstate.Task) (taskstate.Task, error) { return task.PlaceHold(hold) },
	)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}

// ReleaseHold removes a task hold and rebuilds the backlog projection.
func (service *Service) ReleaseHold(ctx context.Context, taskID string) (taskstate.Task, error) {
	updated, err := service.store.Update(ctx, taskID, taskstate.Task.ReleaseHold)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}
