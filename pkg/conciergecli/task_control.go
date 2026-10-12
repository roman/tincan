package conciergecli

import (
	"context"
	"fmt"
	"io"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/taskops"
)

const taskControlHelp = "concierge task stop TASK_ID --generation GEN; concierge task cancel TASK_ID; inspect worker show TASK_ID before retrying"

func (command *taskCommands) runControl(
	ctx context.Context,
	root, selected string,
	output io.Writer,
) int {
	service, err := taskops.New(root)
	if err != nil {
		return fail(output, 2, err, taskControlHelp)
	}
	switch selected {
	case "task stop <task-id>":
		input := command.Stop
		if err := service.Stop(ctx, input.TaskID, input.Generation, herdr.NewClient("herdr")); err != nil {
			return fail(output, 1, err, taskControlHelp)
		}
		if _, err := fmt.Fprintf(output, "task: %s\ngeneration: %s\n", quote(input.TaskID), quote(input.Generation)); err != nil {
			return 1
		}
	case "task cancel <task-id>":
		task, err := service.CancelAdoption(ctx, command.Cancel.TaskID)
		if err != nil {
			return fail(
				output,
				1,
				err,
				taskControlHelp+"; cancellation requires an unbound reservation or an empty queued task",
			)
		}
		if _, err := fmt.Fprintf(output, "task: %s\nstate: %s\n", quote(task.ID()), task.State()); err != nil {
			return 1
		}
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), taskControlHelp)
	}
	return 0
}
