package hostservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
)

func taskOperationEndpoint(
	task taskstate.Task,
	operation Operation,
	generation string,
) (*controllerEndpoint, error) {
	snapshot := task.Snapshot()
	if operation == Stop {
		if err := taskops.CheckStop(task, generation); err != nil {
			return nil, err
		}
		if task.HasReceipt(generation, taskstate.Close) ||
			task.HasReceipt(generation, taskstate.Retain) {
			return nil, nil
		}
	} else {
		if snapshot.Completion == nil || snapshot.Completion.Generation != generation {
			if task.HasReceipt(generation, taskstate.Close) || task.HasReceipt(generation, taskstate.Retain) {
				return nil, nil
			}
			return nil, taskstate.ErrInvalidTransition
		}
		if _, err := task.RetireResult(generation); err != nil {
			return nil, err
		}
	}
	if snapshot.Worker == nil {
		return nil, nil
	}
	return &controllerEndpoint{session: snapshot.Worker.Session, pane: snapshot.Worker.Pane}, nil
}

func (server *Server) withTaskEndpoint(
	ctx context.Context,
	request Request,
	execute func() (Response, error),
) (Response, error) {
	input := request.Transition
	task, err := server.store.Read(ctx, input.TaskID)
	if err != nil {
		return Response{}, err
	}
	endpoint, err := taskOperationEndpoint(task, request.Operation, input.Generation)
	if err != nil {
		return Response{}, err
	}
	// Historical receipts do not act on the current worker or its resident.
	if endpoint == nil {
		return execute()
	}
	release, err := server.controllers.quiesce(
		ctx,
		controllerKey{kind: taskController, id: input.TaskID},
		*endpoint,
	)
	if err != nil {
		var stopped *controllerStoppedError
		if errors.As(err, &stopped) {
			err = fmt.Errorf("task supervision is stopped: %w", err)
		}
		return Response{}, err
	}
	defer release()
	task, err = server.store.Read(ctx, input.TaskID)
	if err == nil {
		worker := task.Snapshot().Worker
		current, checkError := taskOperationEndpoint(task, request.Operation, input.Generation)
		err = checkError
		if err == nil &&
			(worker == nil || worker.Generation != input.Generation || current == nil || *current != *endpoint) {
			err = taskstate.ErrInvalidTransition
		}
	}
	if err != nil {
		return Response{}, fmt.Errorf("task supervision is stopped: %w", err)
	}
	response, err := execute()
	if err != nil {
		err = fmt.Errorf("task supervision is stopped: %w", err)
	}
	return response, err
}

func (server *Server) rejectTaskBoundPane(ctx context.Context, endpoint controllerEndpoint) error {
	tasks, err := server.store.List(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		snapshot := task.Snapshot()
		worker := snapshot.Worker
		if worker != nil && worker.Session == endpoint.session && worker.Pane == endpoint.pane {
			return fmt.Errorf("task-bound pane belongs to %s", task.ID())
		}
		intent := snapshot.LaunchIntent
		if intent != nil && intent.Endpoint != nil && intent.Session == endpoint.session &&
			intent.Endpoint.Pane == endpoint.pane {
			return fmt.Errorf("initial launch pane belongs to %s", task.ID())
		}
	}
	return nil
}
