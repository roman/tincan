package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskstate"
)

const taskControlHelp = "concierge task abandon TASK_ID --generation GEN; concierge task stop TASK_ID --generation GEN; concierge task cancel TASK_ID; inspect worker show TASK_ID before retrying"

func (command *taskCommands) runControl(
	ctx context.Context,
	root, selected string,
	expectations hostservice.Expectations,
	output io.Writer,
) int {
	request := hostservice.Request{
		Expectations: &expectations,
		Transition:   &hostservice.TransitionRequest{},
	}
	switch selected {
	case "task abandon <task-id>":
		request.Operation = hostservice.AbandonLaunch
		request.Transition.TaskID = command.Abandon.TaskID
		request.Transition.Generation = command.Abandon.Generation

	case "task stop <task-id>":
		request.Operation = hostservice.Stop
		request.Transition.TaskID = command.Stop.TaskID
		request.Transition.Generation = command.Stop.Generation
	case "task cancel <task-id>":
		request.Operation = hostservice.Cancel
		request.Transition.TaskID = command.Cancel.TaskID
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), taskControlHelp)
	}
	response, err := hostservice.Call(ctx, filepath.Join(root, "host.sock"), request)
	if err != nil {
		return fail(output, 1, err, taskControlHelp)
	}
	if response.Outcome != hostservice.Completed {
		code := 1
		if response.Outcome == hostservice.Invalid {
			code = 2
		}
		help := taskControlHelp
		if request.Operation == hostservice.Cancel {
			help += "; cancellation requires an unbound reservation or an empty queued task"
		}
		return fail(output, code, errors.New(response.Error), help)
	}
	switch selected {
	case "task abandon <task-id>":
		result := response.Abandoned
		if _, err := fmt.Fprintf(output, "task: %s\ngeneration: %s\nstate: %s\n", quote(command.Abandon.TaskID), quote(result.Generation), response.Transition.State); err != nil {
			return 1
		}
		if result.Worktree != "" {
			if _, err := fmt.Fprintf(output, "retained_worktree: %s\nretained_branch: %s\nhelp: %s\n", quote(result.Worktree), quote(result.Branch), quote("paths belong to the abandoned generation; inspect current ownership before removing the worktree or branch")); err != nil {
				return 1
			}
		}
	case "task stop <task-id>":
		input := command.Stop
		if _, err := fmt.Fprintf(output, "task: %s\ngeneration: %s\n", quote(input.TaskID), quote(input.Generation)); err != nil {
			return 1
		}
	case "task cancel <task-id>":
		task, err := taskstate.NewMigrated(*response.Transition)
		if err != nil {
			return fail(output, 1, err, taskControlHelp)
		}

		if _, err := fmt.Fprintf(output, "task: %s\nstate: %s\n", quote(task.ID()), task.State()); err != nil {
			return 1
		}
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), taskControlHelp)
	}
	return 0
}
