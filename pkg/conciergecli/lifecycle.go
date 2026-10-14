package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskstate"
)

func validateCompletion(mode, closedOn string) error {
	if mode == "retain" {
		if closedOn != "" {
			return fmt.Errorf("--closed-on is forbidden for --mode retain")
		}
		return nil
	}
	if mode != "close" {
		return fmt.Errorf("--mode must be close or retain")
	}
	date, err := time.Parse("2006-01-02", closedOn)
	if err != nil || date.Format("2006-01-02") != closedOn {
		return fmt.Errorf("--closed-on must be a valid date YYYY-MM-DD for --mode close")
	}
	return nil
}

func (command *backlogCommands) runLifecycle(
	ctx context.Context,
	root, selected string,
	output io.Writer,
) int {

	var task taskstate.Task
	var err error
	generation := ""
	request := hostservice.Request{Transition: &hostservice.TransitionRequest{}}
	input := request.Transition
	switch selected {
	case "backlog start <task-id>":
		request.Operation = hostservice.Start
		input.TaskID = command.Start.TaskID
	case "backlog finish <task-id>":
		request.Operation = hostservice.Finish
		input.TaskID = command.Finish.TaskID
		input.Generation = command.Finish.Generation
		input.Mode = taskstate.CompletionMode(command.Finish.Mode)
		input.ClosedOn = command.Finish.ClosedOn
		generation = input.Generation
	case "backlog hold <task-id>":
		request.Operation = hostservice.Hold
		input.TaskID = command.Hold.TaskID
		input.Hold = &taskstate.Hold{
			Reason: command.Hold.Reason,
			Kind:   command.Hold.Kind,
			Until:  command.Hold.Until,
		}
	case "backlog release <task-id>":
		request.Operation = hostservice.Release
		input.TaskID = command.Release.TaskID
	case "backlog release-worktree <task-id>":
		request.Operation = hostservice.ReleaseWorktree
		input.TaskID = command.ReleaseWorktree.TaskID
		input.Generation = command.ReleaseWorktree.Generation
		generation = input.Generation
	case "backlog cleanup <task-id>":
		request.Operation = hostservice.Cleanup
		input.TaskID = command.Cleanup.TaskID
		input.Generation = command.Cleanup.Generation
		generation = input.Generation
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), backlogHelp)
	}
	{
		response, callErr := hostservice.Call(ctx, filepath.Join(root, "host.sock"), request)
		if callErr != nil {
			return fail(
				output,
				1,
				callErr,
				"run concierge host serve outside the model sandbox; this client needs approved unsandboxed execution or verified socket permission; inspect the task and retry the identical request",
			)
		}
		if response.Outcome != hostservice.Completed {
			code := 1
			if response.Outcome == hostservice.Invalid {
				code = 2
			}
			return fail(
				output,
				code,
				errors.New(response.Error),
				"use the current primary session; inspect the task before retrying the identical request",
			)
		}
		task, err = taskstate.NewMigrated(*response.Transition)
	}
	if err != nil {
		return fail(
			output,
			1,
			err,
			"inspect concierge backlog show TASK_ID and worker show TASK_ID; retry the same request after correcting the task or storage error",
		)
	}
	if _, err := fmt.Fprintf(output, "task: %s\nstate: %s\n", quote(task.ID()), task.State()); err != nil {
		return 1
	}
	if generation != "" {
		if _, err := fmt.Fprintf(output, "generation: %s\n", quote(generation)); err != nil {
			return 1
		}
	}
	if lease, found := task.RetiredLease(generation); found && lease.ID != "" {
		if _, err := fmt.Fprintf(output, "lease: %s\nworktree: %s\nbranch: %s\n", lease.State, quote(lease.Path), quote(lease.Branch)); err != nil {
			return 1
		}
	}
	return 0
}
