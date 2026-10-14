package hostservice

import (
	"context"
	"errors"
	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/taskstate"
)

const AbandonLaunch Operation = "abandon_launch"

type AbandonedLaunch = taskstate.LaunchAbandonment

func (server *Server) abandonLaunch(
	ctx context.Context,
	input TransitionRequest,
) (Response, error) {
	task, err := server.store.Read(ctx, input.TaskID)
	if err != nil {
		return Response{}, err
	}
	if result, ok := task.LaunchAbandonment(input.Generation); ok {
		snapshot := task.Snapshot()
		return Response{Transition: &snapshot, Abandoned: &result}, nil
	}
	key := controllerKey{kind: taskController, id: input.TaskID}
	release, err := server.controllers.reserveInitialLaunch(key)
	if err != nil {
		return Response{}, err
	}
	defer release()
	result := &AbandonedLaunch{Generation: input.Generation}
	clearedIntent := false
	task, err = server.store.WithTask(ctx, input.TaskID, func(locked *taskstate.LockedTask) error {
		current := locked.Current()
		if _, err := current.AbandonInitialLaunch(input.Generation); err != nil {
			return err
		}
		intent := current.Snapshot().LaunchIntent
		if receipt, ok := current.LaunchAbandonment(input.Generation); ok {
			*result = receipt
			return nil
		}
		client, err := herdr.NewClient(server.config.HerdrExecutable).ForSession(intent.Session)
		if err != nil {
			return err
		}
		resources := herdr.LaunchResources{Name: intent.Name, Label: intent.Label}
		if intent.Endpoint != nil {
			resources.Tab, resources.Pane = intent.Endpoint.Tab, intent.Endpoint.Pane
		}
		if err := client.CheckLaunchAbsent(ctx, resources); err != nil {
			return err
		}
		if _, err := locked.Apply(func(task taskstate.Task) (taskstate.Task, error) { return task.AbandonInitialLaunch(input.Generation) }); err != nil {
			return err
		}
		receipt, ok := locked.Current().LaunchAbandonment(input.Generation)
		if !ok {
			return taskstate.ErrInvalidTransition
		}
		*result = receipt
		clearedIntent = true
		return nil
	})
	if err != nil {
		return Response{}, err
	}
	if clearedIntent {
		server.controllers.forgetInitialResident(key)
	}
	snapshot := task.Snapshot()
	return Response{Transition: &snapshot, Abandoned: result}, nil
}

func validateAbandonedLaunchResponse(response Response, request Request) error {
	if response.Transition == nil || response.Abandoned == nil ||
		response.Abandoned.Generation != request.Transition.Generation ||
		response.Status != nil ||
		response.Task != nil ||
		response.Assignment != nil ||
		response.Stopped != nil ||
		response.Resolution != nil ||
		response.Control != nil ||
		response.Supervision != nil ||
		response.Error != "" {
		return errors.New("host returned an incomplete abandonment result")
	}
	task, err := taskstate.NewMigrated(*response.Transition)
	if err != nil {
		return err
	}

	receipt, ok := task.LaunchAbandonment(request.Transition.Generation)
	if task.ID() != request.Transition.TaskID || !ok || receipt != *response.Abandoned {
		return errors.New("host returned an unapplied abandonment")
	}
	return nil
}
