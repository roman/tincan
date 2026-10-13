package hostservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/resident"
	"github.com/roman/tincan/pkg/supervise"
	"github.com/roman/tincan/pkg/taskstate"
)

const (
	Supervise       Operation = "supervise"
	StopSupervision Operation = "stop_supervision"
)

type SupervisionSettings struct {
	Interval         *time.Duration `json:"interval,omitempty"`
	RestartTolerance *int           `json:"restart_tolerance,omitempty"`
	RestartWindow    *time.Duration `json:"restart_window,omitempty"`
}

type PaneSupervision struct {
	Pane      string   `json:"pane"`
	Name      string   `json:"name,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	AgentArgs []string `json:"agent_args,omitempty"`
}

type SupervisionRequest struct {
	TaskID   string               `json:"task_id,omitempty"`
	Pane     *PaneSupervision     `json:"pane,omitempty"`
	Settings *SupervisionSettings `json:"settings,omitempty"`
}

type SupervisionResult struct {
	TaskID string `json:"task_id,omitempty"`
	PaneID string `json:"pane_id,omitempty"`
	State  string `json:"state"`
}

func defaultSupervision() supervise.Config {
	return supervise.Config{
		Interval:         5 * time.Second,
		RestartTolerance: 5,
		RestartWindow:    time.Minute,
	}
}

func validateSupervisionConfig(config supervise.Config) error {
	if config.Interval <= 0 || config.RestartWindow <= 0 || config.RestartTolerance < 0 ||
		uint64(config.RestartTolerance) > 1<<32-1 {
		return errors.New("invalid supervision interval, tolerance, or window")
	}
	return nil
}

func validateSupervisionInput(operation Operation, input *SupervisionRequest) error {
	if input == nil || (input.TaskID == "") == (input.Pane == nil) {
		return errors.New("supervision requires exactly one task or pane")
	}
	if input.TaskID != "" {
		if _, err := firstmate.ParseTaskID(input.TaskID); err != nil {
			return err
		}
	} else {
		if _, err := herdr.ParseTarget("validation", input.Pane.Pane); err != nil {
			return err
		}
		if operation == Supervise && (strings.TrimSpace(input.Pane.Name) == "" || strings.TrimSpace(input.Pane.Kind) == "") {
			return errors.New("pane supervision requires a name and kind")
		}
		if operation == StopSupervision && (input.Pane.Name != "" || input.Pane.Kind != "" || len(input.Pane.AgentArgs) > 0) {
			return errors.New("stopping supervision accepts only a pane ID")
		}
	}
	if operation == StopSupervision && input.Settings != nil {
		return errors.New("stopping supervision forbids monitor settings")
	}
	if settings := input.Settings; settings != nil {
		config := defaultSupervision()
		if settings.Interval != nil {
			config.Interval = *settings.Interval
		}
		if settings.RestartTolerance != nil {
			config.RestartTolerance = *settings.RestartTolerance
		}
		if settings.RestartWindow != nil {
			config.RestartWindow = *settings.RestartWindow
		}
		return validateSupervisionConfig(config)
	}
	return nil
}

func (server *Server) checkSupervisionSettings(input *SupervisionRequest) error {
	if input == nil || input.Settings == nil {
		return nil
	}
	expected := input.Settings
	actual := server.config.Supervision
	if (expected.Interval != nil && *expected.Interval != actual.Interval) ||
		(expected.RestartTolerance != nil && *expected.RestartTolerance != actual.RestartTolerance) ||
		(expected.RestartWindow != nil && *expected.RestartWindow != actual.RestartWindow) {
		return errors.New("client supervision settings conflict with host configuration")
	}
	return nil
}

func (server *Server) supervision(
	ctx context.Context,
	operation Operation,
	input SupervisionRequest,
) (Response, error) {
	return server.supervisionWithMode(ctx, operation, input, explicitControllerStart)
}

func (server *Server) supervisionWithMode(
	ctx context.Context,
	operation Operation,
	input SupervisionRequest,
	mode controllerStartMode,
) (Response, error) {
	key := controllerKey{kind: taskController, id: input.TaskID}
	result := SupervisionResult{TaskID: input.TaskID, State: "active"}
	if input.Pane != nil {
		if server.config.Session == "" {
			return Response{}, errors.New("host needs an explicit session for pane supervision")
		}
		key = controllerKey{kind: paneController, id: input.Pane.Pane}
		result = SupervisionResult{PaneID: input.Pane.Pane, State: "active"}
	}
	if operation == StopSupervision {
		if err := server.controllers.stop(ctx, key); err != nil {
			return Response{}, err
		}
		result.State = "stopped"
		return Response{Supervision: &result}, nil
	}
	var endpoint controllerEndpoint
	var prepare func(context.Context) (preparedController, error)
	if input.TaskID != "" {
		task, err := server.store.Read(ctx, input.TaskID)
		if err != nil {
			return Response{}, err
		}
		worker := task.Snapshot().Worker
		if task.State() != taskstate.InFlight || worker == nil || worker.PendingAdoption ||
			task.Snapshot().Completion != nil || task.Snapshot().LaunchIntent != nil {
			return Response{}, taskstate.ErrInvalidTransition
		}
		endpoint = controllerEndpoint{session: worker.Session, pane: worker.Pane}
		prepare = func(ctx context.Context) (preparedController, error) {
			return server.prepareTaskController(ctx, input.TaskID, endpoint)
		}
	} else {
		endpoint = controllerEndpoint{session: server.config.Session, pane: input.Pane.Pane}
		prepare = func(ctx context.Context) (preparedController, error) {
			if err := server.rejectTaskBoundPane(ctx, endpoint); err != nil {
				return preparedController{}, err
			}
			return server.preparePaneController(ctx, *input.Pane)
		}
	}
	specification, _ := json.Marshal(struct {
		TaskID string
		Pane   *PaneSupervision
		Config supervise.Config
	}{input.TaskID, input.Pane, server.config.Supervision})
	start := server.controllers.start
	if mode == initialResidentStart {
		start = server.controllers.startInitialResident
	}
	if _, err := start(ctx, key, endpoint, string(specification), prepare); err != nil {
		return Response{}, err
	}
	return Response{Supervision: &result}, nil
}

func (server *Server) prepareTaskController(
	ctx context.Context,
	taskID string,
	endpoint controllerEndpoint,
) (preparedController, error) {
	lease, err := resident.AcquireTaskLease(server.config.Root, taskID)
	if err != nil {
		return preparedController{}, err
	}
	fail := func(err error) (preparedController, error) {
		return preparedController{}, errors.Join(err, lease.Close())
	}
	task, err := server.store.Read(ctx, taskID)
	if err != nil {
		return fail(err)
	}
	binding := task.Snapshot().Worker
	if task.State() != taskstate.InFlight || task.Snapshot().Completion != nil || binding == nil ||
		task.Snapshot().LaunchIntent != nil ||
		binding.PendingAdoption ||
		binding.Session != endpoint.session ||
		binding.Pane != endpoint.pane {
		return fail(taskstate.ErrInvalidTransition)
	}
	worker, err := server.service.SupervisedWorker(
		taskID,
		server.config.MessageRoot,
		herdr.NewClient(server.config.HerdrExecutable),
	)
	if err != nil {
		return fail(err)
	}
	if err := supervise.Check(ctx, worker); err != nil && !errors.Is(err, supervise.ErrRestarted) {
		return fail(err)
	}
	messages, err := inbox.New(server.config.MessageRoot)
	if err != nil {
		return fail(err)
	}
	recipient, err := inbox.ParseRecipient(taskID)
	if err != nil {
		return fail(err)
	}
	controller := resident.Controller{
		Worker:      worker,
		Supervision: server.config.Supervision,
		Watcher: resident.Watcher{
			Store:     messages,
			Recipient: recipient,
			Target:    taskID,
			Doorbell:  worker,
			Report:    server.report,
		},
	}
	return preparedController{Run: controller.Run, Close: lease.Close}, nil
}

func (server *Server) preparePaneController(
	ctx context.Context,
	input PaneSupervision,
) (preparedController, error) {
	client, err := server.manualClient()
	if err != nil {
		return preparedController{}, err
	}
	worker := manualWorker{client: client, input: input}
	if err := supervise.Check(ctx, worker); err != nil && !errors.Is(err, supervise.ErrRestarted) {
		return preparedController{}, err
	}
	return preparedController{Run: func(ctx context.Context, ready chan<- struct{}) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		close(ready)
		return supervise.Run(ctx, worker, server.config.Supervision)
	}}, nil
}

type manualWorker struct {
	client herdr.Client
	input  PaneSupervision
}

func (worker manualWorker) Observe(ctx context.Context) error {
	agent, err := worker.client.Get(ctx, worker.input.Name)
	if errors.Is(err, herdr.ErrNotFound) {
		return supervise.ErrMissing
	}
	if err != nil {
		return err
	}
	if agent.PaneID != worker.input.Pane || agent.Kind != worker.input.Kind {
		return fmt.Errorf("unexpected agent in pane %s", worker.input.Pane)
	}
	return nil
}
func (worker manualWorker) Restart(ctx context.Context) error {
	return worker.client.StartWithArgs(
		ctx,
		worker.input.Pane,
		worker.input.Name,
		worker.input.Kind,
		worker.input.AgentArgs,
	)
}

func (server *Server) restoreTaskControllers(ctx context.Context) error {
	tasks, err := server.store.List(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		snapshot := task.Snapshot()
		if snapshot.LaunchIntent == nil && !initialResidentEligible(snapshot) {
			continue
		}
		if err := server.controllers.awaitInitialResident(controllerKey{kind: taskController, id: task.ID()}); err != nil {
			return err
		}
	}
	server.reconcileInitialTasks(ctx)
	return ctx.Err()
}

func validateSupervisionResponse(response Response, request Request) error {
	result := response.Supervision
	if result == nil || response.Control != nil || response.Task != nil ||
		response.Transition != nil ||
		response.Assignment != nil ||
		response.Resolution != nil ||
		response.Stopped != nil ||
		response.Error != "" {
		return errors.New("host returned an incomplete supervision result")
	}
	input := request.Supervision
	if input.TaskID != "" {
		if result.TaskID != input.TaskID || result.PaneID != "" {
			return errors.New("host returned a different supervised task")
		}
	} else if result.TaskID != "" || result.PaneID != input.Pane.Pane {
		return errors.New("host returned a different supervised pane")
	}
	state := "active"
	if request.Operation == StopSupervision {
		state = "stopped"
	}
	if result.State != state {
		return errors.New("host returned an unapplied supervision result")
	}
	return nil
}
