package conciergecli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/taskops"
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
	service, err := taskops.New(root)
	if err != nil {
		return fail(output, 2, err, backlogHelp)
	}
	var task taskstate.Task
	generation := ""
	switch selected {
	case "backlog start <task-id>":
		task, err = service.Start(ctx, command.Start.TaskID)
	case "backlog finish <task-id>":
		generation = command.Finish.Generation
		task, err = service.Finish(
			ctx,
			command.Finish.TaskID,
			generation,
			taskstate.CompletionMode(command.Finish.Mode),
			command.Finish.ClosedOn,
		)
	case "backlog cleanup <task-id>":
		generation = command.Cleanup.Generation
		task, err = service.Retire(
			ctx,
			command.Cleanup.TaskID,
			generation,
			herdr.NewClient("herdr"),
		)
	case "backlog hold <task-id>":
		task, err = service.PlaceHold(
			ctx,
			command.Hold.TaskID,
			taskstate.Hold{
				Reason: command.Hold.Reason,
				Kind:   command.Hold.Kind,
				Until:  command.Hold.Until,
			},
		)
	case "backlog release <task-id>":
		task, err = service.ReleaseHold(ctx, command.Release.TaskID)
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), backlogHelp)
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
	return 0
}
