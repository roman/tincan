package hostservice

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
)

const Adopt Operation = "adopt"

func validateAdoptionInput(input *taskops.AdoptionInput) error {
	if input == nil {
		return errors.New("adoption requires a binding")
	}
	if _, err := firstmate.ParseTaskID(input.TaskID); err != nil {
		return err
	}
	if err := firstmate.ValidateSpawnGeneration(input.Generation); err != nil {
		return err
	}
	for _, path := range []string{input.Project, input.Worktree} {
		if _, err := firstmate.ParseAbsolutePath(path); err != nil {
			return err
		}
	}
	if _, err := herdr.ParseTarget(input.Session, input.Pane); err != nil {
		return err
	}
	if strings.TrimSpace(input.Name) == "" || len(input.AgentArgs) == 0 {
		return errors.New("adoption requires a worker name and launch arguments")
	}
	return nil
}

func (server *Server) adopt(ctx context.Context, input taskops.AdoptionInput) (Response, error) {
	release, err := server.controllers.reserveAdoption(
		controllerKey{kind: taskController, id: input.TaskID},
		controllerEndpoint{session: input.Session, pane: input.Pane},
	)
	if err != nil {
		return Response{}, err
	}
	defer release()
	task, err := server.service.AdoptWorker(
		ctx,
		input,
		herdr.NewClient(server.config.HerdrExecutable),
	)
	if err != nil {
		return Response{}, err
	}
	snapshot := task.Snapshot()
	return Response{Transition: &snapshot}, nil
}

func validateAdoptionResponse(response Response, request Request) error {
	if err := validateAdoptionInput(request.Adoption); err != nil {
		return err
	}
	if response.Transition == nil || response.Task != nil || response.Resolution != nil ||
		response.Stopped != nil ||
		response.Assignment != nil ||
		response.Error != "" {
		return errors.New("host returned an incomplete adoption result")
	}
	task, err := taskstate.NewMigrated(*response.Transition)
	if err != nil {
		return err
	}
	input := request.Adoption
	snapshot := task.Snapshot()
	worker := snapshot.Worker
	if task.ID() != input.TaskID ||
		(task.State() != taskstate.Queued && task.State() != taskstate.InFlight) ||
		snapshot.Completion != nil ||
		worker == nil ||
		worker.PendingAdoption ||
		worker.PendingGeneration != "" ||
		worker.PendingLaunch != "" {
		return errors.New("host returned an unapplied adoption")
	}
	if worker.Name != input.Name || worker.Generation != input.Generation ||
		worker.Session != input.Session ||
		worker.Pane != input.Pane ||
		worker.Project != input.Project ||
		worker.Worktree != input.Worktree ||
		!reflect.DeepEqual(worker.AgentArgs, input.AgentArgs) {
		return errors.New("host returned a different worker binding")
	}
	return nil
}
