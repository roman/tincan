package conciergecli

import (
	"context"
	"fmt"
	"io"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/taskops"
)

const taskMessageHelp = "concierge task receive|ack TASK_ID MESSAGE_ID --generation GENERATION; global flags: --root, --message-root, --help"

type assignmentSelection struct {
	TaskID     string `arg:"" help:"Task ID."`
	MessageID  string `arg:"" help:"Assignment message ID."`
	Generation string `required:"" help:"Worker generation supplied at launch."`
}

type taskCommands struct {
	List    struct{}            `cmd:"" help:"List tasks with confirmed worker bindings."`
	Pending struct{}            `cmd:"" help:"List accepted results awaiting completion recovery."`
	Receive assignmentSelection `cmd:"" help:"Read an assignment for the caller's generation."`
	Ack     assignmentSelection `cmd:"" help:"Acknowledge an assignment for the caller's generation."`
}

func (command *cli) runTaskMessages(
	ctx context.Context,
	root, selected string,
	output io.Writer,
) int {
	messageRoot, err := rootWithDefault(command.MessageRoot, ".tincan")
	if err != nil {
		return fail(output, 1, err, "provide an absolute --message-root path")
	}
	if _, err := firstmate.ParseAbsolutePath(messageRoot); err != nil {
		return fail(output, 2, err, "use an absolute, clean --message-root path")
	}
	service, err := taskops.New(root)
	if err != nil {
		return fail(output, 2, err, taskMessageHelp)
	}
	var result taskops.AssignmentStatus
	switch selected {
	case "task receive <task-id> <message-id>":
		input := command.Task.Receive
		result, err = service.ReadAssignment(
			ctx,
			messageRoot,
			input.TaskID,
			input.MessageID,
			input.Generation,
		)
	case "task ack <task-id> <message-id>":
		input := command.Task.Ack
		result, err = service.AcknowledgeAssignment(
			ctx,
			messageRoot,
			input.TaskID,
			input.MessageID,
			input.Generation,
		)
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), taskMessageHelp)
	}
	if err != nil {
		return fail(
			output,
			1,
			err,
			"inspect the task and assignment; use the generation supplied at worker launch",
		)
	}
	if _, err := fmt.Fprintf(output, "status: %s\n", result.State); err != nil {
		return 1
	}
	if result.State == taskops.AssignmentCurrent {
		if _, err := fmt.Fprintf(output, "body: %s\n", quote(result.Body)); err != nil {
			return 1
		}
	}
	return 0
}
