package hostservice

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
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
		Generation: generation, Name: "concierge-" + generation, Label: "concierge-" + generation,
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
	release()
	if err != nil {
		return Response{}, err
	}
	if _, err := server.supervisionWithMode(ctx, Supervise, SupervisionRequest{TaskID: input.TaskID}, initialResidentStart); err != nil {
		return Response{}, err
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
	git := worktree.Client{Executable: server.config.GitExecutable}
	if err := git.Validate(ctx, worktree.Specification{
		Project: input.Project, Path: input.Worktree, Branch: input.Branch, BaseCommit: input.BaseCommit,
	}); err != nil {
		return Response{}, err
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
		return Response{}, err
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
		worker.Worktree != input.Worktree ||
		worker.Harness != input.Harness ||
		worker.Workspace != input.Workspace {
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
		intent.Generation = "s" + rand.Text()
		intent.Name = "concierge-" + intent.Generation
		intent.Label = intent.Name
	}
	return server.reserveLaunchIntent(ctx, input.TaskID, intent)
}
