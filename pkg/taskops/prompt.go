package taskops

import (
	"context"
	"fmt"

	"github.com/roman/tincan/pkg/notify"
	"github.com/roman/tincan/pkg/taskstate"
)

// Prompt implements a task doorbell whose endpoint is resolved on every attempt.
func (worker *SupervisedWorker) Prompt(ctx context.Context, target, text string) error {
	if target != worker.taskID {
		return notify.ErrInvalidTarget
	}
	if text != notify.DoorbellText {
		return fmt.Errorf("task doorbell requires the standard notification text")
	}
	_, err := worker.service.store.WithTask(
		ctx,
		worker.taskID,
		func(task *taskstate.LockedTask) error {
			return worker.promptBoundWorker(ctx, task)
		},
	)
	return err
}
