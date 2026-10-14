package hostservice

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/worktree"
)

const Launch Operation = "launch"

type launchAdmission uint8

const (
	operatorLaunch launchAdmission = iota
	recoveryLaunch
)

var ErrLaunchAlreadyBound = errors.New(
	"task already has a worker; inspect the task before continuing",
)
var ErrLaunchRetired = errors.New("task has a retired launch; inspect the task before continuing")

// LaunchRequest selects work while the host owns launch identity and tools.
type LaunchRequest struct {
	TaskID     string   `json:"task_id"`
	Harness    string   `json:"harness"`
	Project    string   `json:"project"`
	Worktree   string   `json:"worktree"`
	Branch     string   `json:"branch"`
	BaseCommit string   `json:"base_commit"`
	Workspace  string   `json:"workspace"`
	AgentArgs  []string `json:"agent_args,omitempty"`
	Body       string   `json:"body"`
}

func validateLaunchInput(input *LaunchRequest) error {
	if input == nil {
		return errors.New("launch requires a task and launch request")
	}
	if err := taskops.ValidateInitialLaunch(input.Harness, input.AgentArgs, input.Body); err != nil {
		return err
	}
	if input.Branch == "HEAD" || input.BaseCommit != strings.ToLower(input.BaseCommit) {
		return errors.New("launch requires a literal branch and lowercase full commit ID")
	}
	task, err := taskstate.NewQueued(
		taskstate.Definition{
			ID:    input.TaskID,
			Title: "validation",
			Repo:  "validation",
			Kind:  "task",
		},
	)
	if err != nil {
		return err
	}
	_, err = task.ReserveInitialLaunch(input.intent("validation", "validation"))
	return err
}

func (input LaunchRequest) intent(session, generation string) taskstate.LaunchIntent {
	return taskstate.LaunchIntent{
		Generation: generation, Name: "c-" + strings.ToLower(generation), Label: "concierge-" + generation,
		Harness: input.Harness, Session: session,
		Project: input.Project, Worktree: input.Worktree, Branch: input.Branch,
		BaseCommit: input.BaseCommit, Workspace: input.Workspace,
		AgentArgs: slices.Clone(input.AgentArgs), Body: input.Body,
	}
}

func (server *Server) launch(ctx context.Context, input LaunchRequest) (Response, error) {
	release, err := server.controllers.reserveInitialLaunch(
		controllerKey{kind: taskController, id: input.TaskID},
	)
	if err != nil {
		return Response{}, err
	}
	defer release()
	response, err := server.executeInitialLaunch(ctx, input, operatorLaunch)
	if err != nil {
		tracked, inspectionErr := server.trackedLaunchResult(ctx, input, err)
		if inspectionErr == nil {
			tracked.Checkpoint = response.Checkpoint
		}
		return tracked, inspectionErr
	}
	release()
	if _, err := server.supervisionWithMode(ctx, Supervise, SupervisionRequest{TaskID: input.TaskID}, initialResidentStart); err != nil {
		return server.launchRegistrationFailure(input, response, err)
	}
	return response, nil
}

// executeInitialLaunch requires a task operation reservation throughout execution.
func (server *Server) executeInitialLaunch(
	ctx context.Context,
	input LaunchRequest,
	admission launchAdmission,
) (Response, error) {
	if _, err := firstmate.ParseAbsolutePath(server.config.GitExecutable); err != nil {
		return Response{}, errors.New("host launch requires an absolute Git executable")
	}
	if server.config.Session == "" {
		return Response{}, errors.New("host launch requires an explicit Herdr session")
	}
	existing, err := server.store.Read(ctx, input.TaskID)
	if err != nil {
		return Response{}, err
	}
	if err := completedLaunchConflict(existing.Snapshot()); err != nil {
		return Response{}, err
	}
	if err := validateLaunchInput(&input); err != nil {
		return Response{}, err
	}
	candidate := input.intent(server.config.Session, "validation")
	if current := existing.Snapshot().LaunchIntent; current != nil {
		candidate.Generation, candidate.Name, candidate.Label = current.Generation, current.Name, current.Label
	}
	if _, err := existing.ReserveInitialLaunch(candidate); err != nil {
		return Response{}, err
	}
	if existing.Snapshot().LaunchIntent == nil && input.Worktree != "" {
		return Response{}, errors.New(
			"fresh launches use a treehouse lease; omit the worktree path",
		)
	}
	if input.Worktree == "" {
		if _, err := firstmate.ParseAbsolutePath(server.config.TreehouseExecutable); err != nil {
			return Response{}, errors.New("host launch requires an absolute treehouse executable")
		}
	}
	validationPath := input.Worktree
	if validationPath == "" {
		validationPath = input.Project
	}
	git := worktree.Client{Executable: server.config.GitExecutable}
	if err := git.Validate(ctx, worktree.Specification{
		Project: input.Project, Path: validationPath, Branch: input.Branch, BaseCommit: input.BaseCommit,
	}); err != nil {
		return Response{}, err
	}
	if existing.Snapshot().LaunchIntent == nil && input.Workspace != "" {
		client, err := herdr.NewClient(server.config.HerdrExecutable).
			ForSession(server.config.Session)
		if err != nil {
			return Response{}, err
		}
		if err := client.ValidateWorkspace(ctx, input.Workspace); err != nil {
			return Response{}, err
		}
	}
	if admission == operatorLaunch {
		if err := server.controllers.awaitInitialResident(controllerKey{kind: taskController, id: input.TaskID}); err != nil {
			return Response{}, err
		}
	}
	task, err := server.reserveInitialLaunch(ctx, input)
	if err != nil {
		return Response{}, err
	}
	var releaseEndpoint func()
	defer func() {
		if releaseEndpoint != nil {
			releaseEndpoint()
		}
	}()
	task, err = server.service.ContinueInitialLaunch(
		ctx,
		input.TaskID,
		task.Snapshot().LaunchIntent.Generation,
		taskops.InitialLaunchConfig{
			Pool:        worktree.PoolClient{Executable: server.config.TreehouseExecutable},
			Git:         git,
			Herdr:       herdr.NewClient(server.config.HerdrExecutable),
			MessageRoot: server.config.MessageRoot,
			ReserveEndpoint: func(endpoint taskstate.LaunchEndpoint) error {
				var err error
				releaseEndpoint, err = server.controllers.acquire(
					controllerEndpoint{session: server.config.Session, pane: endpoint.Pane},
				)
				return err
			},
		},
	)
	if err != nil {
		if !errors.Is(err, taskops.ErrNativeCheckpoint) {
			return Response{}, err
		}
		checkpoint, noticeErr := server.launchNativeCheckpoint(ctx, input.TaskID)
		if checkpoint != nil {
			err = fmt.Errorf(
				"native checkpoint (%s) requires operator review: %w",
				checkpoint.Kind,
				errors.Join(err, noticeErr),
			)
		}
		return Response{Checkpoint: checkpoint}, err
	}
	snapshot := task.Snapshot()
	return Response{Transition: &snapshot}, nil
}

func completedLaunchConflict(snapshot taskstate.Snapshot) error {
	if snapshot.LaunchIntent != nil {
		return nil
	}
	if snapshot.Worker != nil {
		return ErrLaunchAlreadyBound
	}
	if snapshot.Receipt != nil || len(snapshot.PriorReceipts) != 0 {
		return ErrLaunchRetired
	}
	return nil
}

func validateLaunchResponse(response Response, request Request) error {
	if response.Transition == nil || response.Task != nil || response.Assignment != nil ||
		response.Resolution != nil ||
		response.Stopped != nil ||
		response.Error != "" {
		return errors.New("host returned an incomplete launch result")
	}
	task, err := taskstate.NewMigrated(*response.Transition)
	if err != nil {
		return err
	}
	snapshot := task.Snapshot()
	worker := snapshot.Worker
	input := request.Launch
	if snapshot.State != taskstate.InFlight || snapshot.LaunchIntent != nil ||
		snapshot.Completion != nil ||
		worker == nil ||
		worker.PendingAdoption ||
		worker.PendingGeneration != "" ||
		worker.PendingLaunch != "" {
		return errors.New("host returned an unapplied launch")
	}
	if task.ID() != input.TaskID || worker.Project != input.Project ||
		(input.Worktree != "" && worker.Worktree != input.Worktree) ||
		(input.Worktree == "" && (worker.Lease.State != taskstate.LeaseHeld || worker.Lease.Branch != input.Branch || worker.Lease.BaseCommit != input.BaseCommit)) ||
		worker.Harness != input.Harness ||
		(input.Workspace != "" && (worker.Workspace != input.Workspace || worker.OwnedWorkspaceLabel != "")) ||
		(input.Workspace == "" && worker.OwnedWorkspaceLabel != "concierge-"+worker.Generation) {
		return errors.New("host returned a different launched worker")
	}
	return nil
}

// reserveInitialLaunch requires the caller's task operation reservation. Pending
// retries retain identity; completed requests require inspection after lost replies.
func (server *Server) reserveInitialLaunch(
	ctx context.Context,
	input LaunchRequest,
) (taskstate.Task, error) {
	if err := validateLaunchInput(&input); err != nil {
		return taskstate.Task{}, err
	}
	task, err := server.store.Read(ctx, input.TaskID)
	if err != nil {
		return taskstate.Task{}, err
	}
	snapshot := task.Snapshot()
	intent := input.intent(server.config.Session, "")
	if current := snapshot.LaunchIntent; current != nil {
		intent.Generation, intent.Name, intent.Label = current.Generation, current.Name, current.Label
	} else {
		if err := completedLaunchConflict(snapshot); err != nil {
			return taskstate.Task{}, err
		}
		intent = input.intent(server.config.Session, "s"+rand.Text())
	}
	if err := herdr.ValidateAgentName(intent.Name); err != nil {
		return taskstate.Task{}, err
	}
	return server.reserveLaunchIntent(ctx, input.TaskID, intent)
}
