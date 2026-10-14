package taskops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
)

// AdoptionInput identifies an existing worker and its recoverable launch command.
type AdoptionInput struct {
	TaskID     string
	Generation string
	Name       string
	Session    string
	Pane       string
	Project    string
	Worktree   string
	AgentArgs  []string
}

// AdoptWorker reserves and confirms a live worker without changing task dispatch.
func (service *Service) AdoptWorker(
	ctx context.Context,
	input AdoptionInput,
	client herdr.Client,
) (taskstate.Task, error) {
	return service.adoptWorker(ctx, input, client, service.store.ReserveAdoption)
}

func (service *Service) adoptWorker(
	ctx context.Context,
	input AdoptionInput,
	client herdr.Client,
	reserve func(context.Context, string, taskstate.Worker) (taskstate.Task, error),
) (taskstate.Task, error) {
	if err := firstmate.ValidateSpawnGeneration(input.Generation); err != nil {
		return taskstate.Task{}, fmt.Errorf("invalid adoption generation: %w", err)
	}
	for _, path := range []string{input.Project, input.Worktree} {
		if _, err := firstmate.ParseAbsolutePath(path); err != nil {
			return taskstate.Task{}, err
		}
	}
	if _, err := herdr.ParseTarget(input.Session, input.Pane); err != nil {
		return taskstate.Task{}, err
	}
	boundClient, err := client.ForSession(input.Session)
	if err != nil {
		return taskstate.Task{}, err
	}
	current, err := service.store.Read(ctx, input.TaskID)
	if err != nil {
		return taskstate.Task{}, err
	}
	if !adoptionEligible(current) {
		return taskstate.Task{}, taskstate.ErrInvalidTransition
	}
	bound := current.Snapshot().Worker
	if bound == nil {
		_, harness, err := verifyAdoption(ctx, input, boundClient)
		if err != nil {
			return taskstate.Task{}, err
		}
		worker := taskstate.Worker{
			Name:       input.Name,
			Harness:    harness,
			Session:    input.Session,
			Pane:       input.Pane,
			Project:    input.Project,
			Worktree:   input.Worktree,
			Generation: input.Generation,
			AgentArgs:  input.AgentArgs,
		}
		reserved, err := reserve(ctx, input.TaskID, worker)
		if err != nil {
			return reserved, err
		}
		bound = reserved.Snapshot().Worker
	}
	if !matchesAdoptionInput(*bound, input) {
		return taskstate.Task{}, taskstate.ErrInvalidTransition
	}
	updated, err := service.store.Update(
		ctx,
		input.TaskID,
		func(task taskstate.Task) (taskstate.Task, error) {
			if !adoptionEligible(task) ||
				!matchesAdoptionReservation(*bound, task.Snapshot().Worker) {
				return taskstate.Task{}, taskstate.ErrInvalidTransition
			}
			currentWorker := task.Snapshot().Worker
			details, harness, err := verifyAdoption(ctx, input, boundClient)
			if err != nil {
				return taskstate.Task{}, err
			}
			if harness != bound.Harness ||
				(currentWorker.Workspace != "" && currentWorker.Workspace != details.WorkspaceID) ||
				(currentWorker.Tab != "" && currentWorker.Tab != details.TabID) {
				return taskstate.Task{}, taskstate.ErrInvalidTransition
			}
			if task.State() == taskstate.InFlight {
				return task, nil
			}
			return task.ConfirmAdoption(details.WorkspaceID, details.TabID)
		},
	)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}

func adoptionEligible(task taskstate.Task) bool {
	snapshot := task.Snapshot()
	if snapshot.Completion != nil ||
		(snapshot.State != taskstate.Queued && snapshot.State != taskstate.InFlight) {
		return false
	}
	worker := snapshot.Worker
	return worker == nil ||
		(worker.PendingGeneration == "" && worker.PendingLaunch == "" && (!worker.PendingAdoption || snapshot.State == taskstate.Queued))
}

func matchesAdoptionReservation(captured taskstate.Worker, current *taskstate.Worker) bool {
	if current == nil || (!captured.PendingAdoption && current.PendingAdoption) {
		return false
	}
	if captured.PendingAdoption {
		captured.PendingAdoption = current.PendingAdoption
		if captured.Workspace == "" {
			captured.Workspace = current.Workspace
			captured.Tab = current.Tab
		}
	}
	return reflect.DeepEqual(captured, *current)
}

func matchesAdoptionInput(worker taskstate.Worker, input AdoptionInput) bool {
	return worker.Generation == input.Generation &&
		worker.Name == input.Name && worker.Session == input.Session &&
		worker.Pane == input.Pane &&
		worker.Project == input.Project &&
		worker.Worktree == input.Worktree &&
		reflect.DeepEqual(worker.AgentArgs, input.AgentArgs)
}

func verifyAdoption(
	ctx context.Context,
	input AdoptionInput,
	client herdr.Client,
) (herdr.PaneDetails, string, error) {
	details, err := client.PaneDetails(ctx, input.Pane)
	if err != nil {
		return herdr.PaneDetails{}, "", err
	}
	if err := verifyWorktree(input.Worktree, details.ForegroundDirectory); err != nil {
		return herdr.PaneDetails{}, "", err
	}
	agent, err := client.Get(ctx, input.Name)
	if err != nil {
		return herdr.PaneDetails{}, "", err
	}
	if err := validateNativeLaunch(agent.Kind, input.AgentArgs); err != nil {
		return herdr.PaneDetails{}, "", err
	}
	if agent.PaneID != input.Pane || agent.Kind == "" {
		return herdr.PaneDetails{}, "", fmt.Errorf("worker identity differs from requested pane")
	}
	target, err := herdr.ParseTarget(input.Session, input.Pane)
	if err != nil {
		return herdr.PaneDetails{}, "", err
	}
	if health := client.Health(ctx, target); health.Endpoint != herdr.Alive {
		return herdr.PaneDetails{}, "", fmt.Errorf("worker endpoint is %s", health.Endpoint)
	}
	return details, agent.Kind, nil
}

func verifyWorktree(worktree, directory string) error {
	recorded, err := os.Stat(worktree)
	if err != nil {
		return err
	}
	live, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !recorded.IsDir() || !live.IsDir() || !os.SameFile(recorded, live) {
		return fmt.Errorf("pane is outside task worktree")
	}
	return nil
}
