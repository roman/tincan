package taskops

import (
	"context"
	"fmt"

	"github.com/roman/tincan/pkg/herdr"
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
			_, err := worker.promptBoundWorker(ctx, task)
			return err
		},
	)
	return err
}

func (worker *SupervisedWorker) notificationEndpoint(
	ctx context.Context,
	task *taskstate.LockedTask,
) (herdr.Client, herdr.Agent, error) {
	bound, err := assignmentWorker(task.Current())
	if err != nil {
		return herdr.Client{}, herdr.Agent{}, err
	}
	client, err := worker.boundClient(ctx, bound)
	if err != nil {
		return herdr.Client{}, herdr.Agent{}, err
	}
	agent, err := client.Get(ctx, bound.Name)
	if err != nil {
		return herdr.Client{}, herdr.Agent{}, err
	}
	if agent.PaneID != bound.Pane || agent.Kind != bound.Harness {
		return herdr.Client{}, herdr.Agent{}, fmt.Errorf(
			"unexpected agent for task %s",
			worker.taskID,
		)
	}
	if err := task.CheckCurrent(); err != nil {
		return herdr.Client{}, herdr.Agent{}, err
	}
	return client, agent, nil
}
