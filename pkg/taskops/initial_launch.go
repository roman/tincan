package taskops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/worktree"
)

var ErrInitialLaunchIdentity = errors.New("initial launch identity conflict")

type InitialLaunchConfig struct {
	Git         worktree.Client
	Herdr       herdr.Client
	MessageRoot string
	// ReserveEndpoint must keep the endpoint reserved until this call returns.
	ReserveEndpoint func(taskstate.LaunchEndpoint) error
}

// ValidateInitialLaunch rejects requests that cannot dispatch after startup.
func ValidateInitialLaunch(harness string, arguments []string, body string) error {
	if strings.TrimSpace(body) == "" {
		return inbox.ErrBlankBody
	}
	if len(arguments) != 0 && !strings.HasPrefix(arguments[0], "-") {
		return fmt.Errorf(
			"initial worker arguments must start with an option; Concierge supplies the protocol prompt",
		)
	}
	return validateNativeLaunch(harness, arguments)
}

// ContinueInitialLaunch executes one captured intent. The host must reserve the task
// operation throughout this call so another executor cannot submit the same effect.
func (service *Service) ContinueInitialLaunch(
	ctx context.Context,
	taskID, generation string,
	config InitialLaunchConfig,
) (taskstate.Task, error) {
	if config.ReserveEndpoint == nil {
		return taskstate.Task{}, errors.New("initial launch requires endpoint reservation")
	}
	if _, err := firstmate.ParseAbsolutePath(config.MessageRoot); err != nil {
		return taskstate.Task{}, err
	}
	_, intent, err := service.initialLaunch(ctx, taskID, generation)
	if err != nil {
		return taskstate.Task{}, err
	}
	if err := ValidateInitialLaunch(intent.Harness, intent.AgentArgs, intent.Body); err != nil {
		return taskstate.Task{}, err
	}
	if intent.Endpoint != nil {
		if err := config.ReserveEndpoint(*intent.Endpoint); err != nil {
			return taskstate.Task{}, err
		}
	}
	protocol, err := service.workerProtocol(taskID, config.MessageRoot)
	if err != nil {
		return taskstate.Task{}, err
	}
	arguments, err := nativeLaunchArguments(intent.Harness, intent.AgentArgs, generation, protocol)
	if err != nil {
		return taskstate.Task{}, err
	}
	client, err := config.Herdr.ForSession(intent.Session)
	if err != nil {
		return taskstate.Task{}, err
	}
	if err := service.prepareInitialPane(ctx, taskID, intent, config, client); err != nil {
		return taskstate.Task{}, err
	}
	_, intent, err = service.initialLaunch(ctx, taskID, generation)
	if err != nil {
		return taskstate.Task{}, err
	}
	if err := service.prepareInitialAgent(ctx, taskID, intent, config.Git, client, arguments); err != nil {
		return taskstate.Task{}, err
	}
	_, intent, err = service.initialLaunch(ctx, taskID, generation)
	if err != nil {
		return taskstate.Task{}, err
	}
	agent, err := client.Get(ctx, intent.Name)
	if err != nil {
		return taskstate.Task{}, fmt.Errorf("initial agent identity remains unresolved: %w", err)
	}
	if agent.PaneID != intent.Endpoint.Pane || agent.Kind != intent.Harness {
		return taskstate.Task{}, fmt.Errorf(
			"%w: initial agent differs from reserved endpoint or harness", ErrInitialLaunchIdentity,
		)
	}
	resume, err := nativeResumeArguments(intent.Harness, agent.Session)
	if err != nil {
		if agent.Session != nil {
			return taskstate.Task{}, fmt.Errorf("%w: %w", ErrInitialLaunchIdentity, err)
		}
		return taskstate.Task{}, err
	}
	if err := config.Git.VerifyIdentity(ctx, launchWorktree(intent)); err != nil {
		return taskstate.Task{}, err
	}
	if _, err := service.store.Update(ctx, taskID, func(task taskstate.Task) (taskstate.Task, error) {
		return task.RecordLaunchResume(generation, resume)
	}); err != nil {
		return taskstate.Task{}, err
	}
	if err := service.checkInitialLaunchActive(ctx, taskID, generation); err != nil {
		return taskstate.Task{}, err
	}
	adopted, err := service.AdoptWorker(ctx, AdoptionInput{
		TaskID: taskID, Generation: generation, Name: intent.Name, Session: intent.Session,
		Pane: intent.Endpoint.Pane, Project: intent.Project, Worktree: intent.Worktree, AgentArgs: resume,
	}, config.Herdr)
	if err != nil {
		return adopted, err
	}
	if adopted.State() == taskstate.Queued {
		if _, err := service.Start(ctx, taskID); err != nil {
			return taskstate.Task{}, err
		}
	}
	if err := service.checkInitialLaunchActive(ctx, taskID, generation); err != nil {
		return taskstate.Task{}, err
	}
	if _, err := service.SendAssignment(ctx, config.MessageRoot, taskID, "initial-launch", intent.Body, config.Herdr); err != nil {
		return taskstate.Task{}, err
	}
	return service.store.Update(ctx, taskID, func(task taskstate.Task) (taskstate.Task, error) {
		return task.CompleteInitialLaunch(generation)
	})
}

func launchWorktree(intent *taskstate.LaunchIntent) worktree.Specification {
	return worktree.Specification{
		Project:    intent.Project,
		Path:       intent.Worktree,
		Branch:     intent.Branch,
		BaseCommit: intent.BaseCommit,
	}
}

func (service *Service) initialLaunch(
	ctx context.Context,
	taskID, generation string,
) (taskstate.Task, *taskstate.LaunchIntent, error) {
	task, err := service.store.Read(ctx, taskID)
	if err != nil {
		return taskstate.Task{}, nil, err
	}
	snapshot := task.Snapshot()
	if snapshot.LaunchIntent == nil || snapshot.LaunchIntent.Generation != generation {
		return taskstate.Task{}, nil, taskstate.ErrInvalidTransition
	}
	return task, snapshot.LaunchIntent, nil
}

func (service *Service) checkInitialLaunchActive(
	ctx context.Context,
	taskID, generation string,
) error {
	task, _, err := service.initialLaunch(ctx, taskID, generation)
	if err != nil {
		return err
	}
	if task.Definition().Hold != nil {
		return taskstate.ErrInvalidTransition
	}
	return nil
}

func (service *Service) prepareInitialPane(
	ctx context.Context,
	taskID string,
	intent *taskstate.LaunchIntent,
	config InitialLaunchConfig,
	client herdr.Client,
) error {
	generation := intent.Generation
	if intent.Phase == taskstate.LaunchReserved {
		if _, err := service.store.Update(ctx, taskID, func(task taskstate.Task) (taskstate.Task, error) {
			return task.BeginLaunchPreparation(generation)
		}); err != nil {
			return err
		}
		intent.Phase = taskstate.LaunchPreparing
	}
	var details herdr.PaneDetails
	if intent.Phase == taskstate.LaunchPreparing {
		if err := service.checkInitialLaunchActive(ctx, taskID, generation); err != nil {
			return err
		}
		if err := config.Git.Prepare(ctx, launchWorktree(intent)); err != nil {
			return err
		}
		if _, err := service.store.Update(ctx, taskID, func(task taskstate.Task) (taskstate.Task, error) {
			return task.RequestLaunchPane(generation)
		}); err != nil {
			return err
		}
		if err := service.checkInitialLaunchActive(ctx, taskID, generation); err != nil {
			return err
		}
		var err error
		details, err = client.CreateTab(ctx, herdr.TabInput{
			Workspace: intent.Workspace, Directory: intent.Worktree, Label: intent.Label,
			Environment: []string{
				"CONCIERGE_WORKER=1",
				"CONCIERGE_HOME=" + service.root,
				"TINCAN_HOME=" + config.MessageRoot,
				"CONCIERGE_TASK_ID=" + taskID,
			},
		})
		if err != nil {
			return err
		}
	} else if intent.Endpoint == nil {
		var err error
		details, err = client.FindTab(ctx, intent.Workspace, intent.Label)
		if err != nil {
			return fmt.Errorf("initial pane creation remains unresolved: %w", err)
		}
	} else {
		return nil
	}
	if err := verifyWorktree(intent.Worktree, details.ForegroundDirectory); err != nil {
		return fmt.Errorf("%w: %w", ErrInitialLaunchIdentity, err)
	}
	endpoint := taskstate.LaunchEndpoint{
		Workspace: details.WorkspaceID, Tab: details.TabID, Pane: details.PaneID,
	}
	if err := config.ReserveEndpoint(endpoint); err != nil {
		return err
	}
	_, err := service.store.RecordLaunchEndpoint(ctx, taskID, generation, endpoint)
	return err
}

func (service *Service) prepareInitialAgent(
	ctx context.Context,
	taskID string,
	intent *taskstate.LaunchIntent,
	git worktree.Client,
	client herdr.Client,
	arguments []string,
) error {
	details, err := client.PaneDetails(ctx, intent.Endpoint.Pane)
	if err != nil {
		return err
	}
	if details.WorkspaceID != intent.Endpoint.Workspace || details.TabID != intent.Endpoint.Tab {
		return fmt.Errorf(
			"%w: initial launch pane moved from the recorded location",
			ErrInitialLaunchIdentity,
		)
	}
	if err := verifyWorktree(intent.Worktree, details.ForegroundDirectory); err != nil {
		return fmt.Errorf("%w: %w", ErrInitialLaunchIdentity, err)
	}
	if intent.Phase == taskstate.LaunchAgentRequested {
		return nil
	}
	if err := service.checkInitialLaunchActive(ctx, taskID, intent.Generation); err != nil {
		return err
	}
	if err := git.Prepare(ctx, launchWorktree(intent)); err != nil {
		return err
	}
	if _, err := client.Get(ctx, intent.Name); err == nil {
		return herdr.ErrAgentPresent
	} else if !errors.Is(err, herdr.ErrNotFound) {
		return err
	}
	if _, err := service.store.Update(ctx, taskID, func(task taskstate.Task) (taskstate.Task, error) {
		return task.RequestLaunchAgent(intent.Generation)
	}); err != nil {
		return err
	}
	if err := service.checkInitialLaunchActive(ctx, taskID, intent.Generation); err != nil {
		return err
	}
	return client.StartWithArgs(ctx, intent.Endpoint.Pane, intent.Name, intent.Harness, arguments)
}
