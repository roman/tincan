package taskops

import (
	"context"
	"errors"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
)

// Retire clears a completed worker only after its exact pane has departed.
// Receipt retries republish the record without checking a newer worker's pane.
func (service *Service) Retire(
	ctx context.Context, taskID, generation string, client herdr.Client,
) (taskstate.Task, error) {
	updated, err := service.store.Update(
		ctx,
		taskID,
		func(task taskstate.Task) (taskstate.Task, error) {
			snapshot := task.Snapshot()
			if snapshot.Completion == nil || snapshot.Completion.Generation != generation {
				if task.HasReceipt(generation, taskstate.Close) ||
					task.HasReceipt(generation, taskstate.Retain) {
					if task.HasReceipt(generation, taskstate.Close) {
						lease, _ := task.RetiredLease(generation)
						if lease != (taskstate.WorktreeLease{}) {
							return service.returnRetiredLease(ctx, task, generation)
						}
					}
					return task, nil
				}
				return taskstate.Task{}, taskstate.ErrInvalidTransition
			}
			retired, err := task.RetireResult(generation)
			if err != nil {
				return taskstate.Task{}, err
			}
			if snapshot.Worker != nil {
				target, err := herdr.ParseTarget(snapshot.Worker.Session, snapshot.Worker.Pane)
				if err != nil {
					return taskstate.Task{}, err
				}
				if err := client.ConfirmPaneGone(ctx, target); err != nil {
					return taskstate.Task{}, err
				}
			}
			if snapshot.Completion.Mode == taskstate.Close && snapshot.Worker != nil &&
				snapshot.Worker.Lease != (taskstate.WorktreeLease{}) {
				return service.returnRetiredLease(ctx, retired, generation)
			}
			return retired, nil
		},
	)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}
