package taskops

import (
	"context"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/presentation"
	"github.com/roman/tincan/pkg/taskstate"
)

// Stop closes a generation's confirmed pane while preserving its task record.
// Cleanup remains separate so failed focus restoration can be retried.
func (service *Service) Stop(
	ctx context.Context,
	taskID, generation string,
	client herdr.Client,
) error {
	if err := firstmate.ValidateSpawnGeneration(generation); err != nil {
		return err
	}
	_, err := service.store.WithTask(ctx, taskID, func(task *taskstate.LockedTask) error {
		if err := task.CheckCurrent(); err != nil {
			return err
		}
		current := task.Current()
		if current.HasReceipt(generation, taskstate.Close) ||
			current.HasReceipt(generation, taskstate.Retain) {
			return nil
		}
		worker := current.Snapshot().Worker
		if worker == nil || worker.Generation != generation || worker.PendingAdoption ||
			worker.PendingGeneration != "" ||
			worker.PendingLaunch != "" ||
			worker.Workspace == "" ||
			worker.Tab == "" {
			return taskstate.ErrInvalidTransition
		}
		target, err := herdr.ParseTarget(worker.Session, worker.Pane)
		if err != nil {
			return err
		}
		return presentation.CloseBoundPane(
			ctx,
			client,
			target,
			worker.Workspace,
			worker.Tab,
			task.CheckCurrent,
		)
	})
	return err
}
