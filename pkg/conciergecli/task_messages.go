package conciergecli

import (
	"context"
	"fmt"
	"io"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskops"
)

const taskMessageHelp = "concierge task send TASK_ID [--key KEY] --body-file FILE|-; concierge task receive|ack TASK_ID MESSAGE_ID --generation GENERATION; global flags: --root, --message-root, --recipient, --help; " + replySendUsage + "; " + replyCheckUsage

type assignmentSelection struct {
	TaskID     string `arg:"" help:"Task ID."`
	MessageID  string `arg:"" help:"Assignment message ID."`
	Generation string `required:"" help:"Worker generation supplied at launch."`
}

type taskCommands struct {
	Stop struct {
		TaskID     string `arg:"" help:"Task ID."`
		Generation string `required:"" help:"Worker generation to stop."`
	} `cmd:"" help:"Close the exact confirmed pane for a worker generation."`
	Cancel struct {
		TaskID string `arg:"" help:"Task ID."`
	} `cmd:"" help:"Release an unbound adoption reservation; reject confirmed or published bindings."`

	Adopt adoptCommand `cmd:"" help:"Verify and bind an existing worker to a queued or running task."`

	Reply replyCommands `cmd:"" help:"Send worker results or check a primary reply."`

	Send struct {
		TaskID   string `arg:"" help:"Task ID."`
		Key      string `help:"Stable logical assignment key for safe retries; omit for an independent message."`
		BodyFile string `required:"" help:"Assignment body file; use - for stdin."`
	} `cmd:"" help:"Publish a generation-bound assignment and notify its worker."`

	List    struct{}            `cmd:"" help:"List tasks with confirmed worker bindings."`
	Pending struct{}            `cmd:"" help:"List accepted results awaiting completion recovery."`
	Receive assignmentSelection `cmd:"" help:"Read an assignment for the caller's generation."`
	Ack     assignmentSelection `cmd:"" help:"Acknowledge an assignment for the caller's generation."`
}

func (command *cli) runTaskMessages(
	ctx context.Context,
	root, selected string,
	stdin io.Reader,
	output io.Writer,
) int {
	if selected == "task send <task-id>" {
		return command.Task.sendAssignment(ctx, root, command.hostExpectations, stdin, output)
	}
	if err := command.validateWorkerLaunch(selected); err != nil {
		return fail(
			output,
			1,
			err,
			"request a controlled worker relaunch with native generation settings; never replace launch context from a transcript, task record, or mailbox",
		)
	}
	messageRoot, err := rootWithDefault(command.MessageRoot, ".tincan")
	if err != nil {
		return fail(output, 1, err, "provide an absolute --message-root path")
	}
	if _, err := firstmate.ParseAbsolutePath(messageRoot); err != nil {
		return fail(output, 2, err, "use an absolute, clean --message-root path")
	}
	var service *taskops.Service
	switch selected {
	case "task reply send <task-id>", "task reply check <message-id>":
		recipient, parseErr := inbox.ParseRecipient(command.Recipient)
		if parseErr != nil {
			return fail(output, 2, parseErr, "use a valid primary --recipient name")
		}
		service, err = taskops.NewWithPrimaryRecipient(root, recipient)
	default:
		service, err = taskops.New(root)
	}
	if err != nil {
		return fail(output, 2, err, taskMessageHelp)
	}
	var result taskops.AssignmentStatus
	switch selected {
	case "task reply send <task-id>":
		return command.Task.Reply.send(ctx, service, messageRoot, stdin, output)
	case "task reply check <message-id>":
		return command.Task.Reply.check(ctx, service, messageRoot, output)
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
