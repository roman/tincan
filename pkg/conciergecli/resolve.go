package conciergecli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
)

func (command *cli) runResolve(ctx context.Context, root string, output io.Writer) int {
	recipient, err := inbox.ParseRecipient(command.Recipient)
	if err != nil {
		return fail(output, 2, err, "use a valid primary --recipient name")
	}
	messageRoot := command.MessageRoot
	if messageRoot == "" {
		directory, err := os.Getwd()
		if err != nil {
			return fail(output, 1, err, "provide an absolute --message-root path")
		}
		messageRoot = filepath.Join(directory, ".tincan")
	}
	if _, err := firstmate.ParseAbsolutePath(messageRoot); err != nil {
		return fail(output, 2, err, "use an absolute, clean --message-root path")
	}
	service, err := taskops.NewWithPrimaryRecipient(root, recipient)
	if err != nil {
		return fail(output, 2, err, backlogHelp)
	}
	result, err := service.Resolve(
		ctx,
		messageRoot,
		command.Backlog.Resolve.ReplyID,
		taskstate.CompletionMode(command.Backlog.Resolve.Mode),
		command.Backlog.Resolve.ClosedOn,
	)
	if err != nil {
		return fail(
			output,
			1,
			err,
			resolveUsage+"; inspect the bound reply and task; retry the same reply, mode, and closing date after correcting the error",
		)
	}
	if _, err := fmt.Fprintf(output, "task: %s\nstate: %s\nalready: %t\n", quote(result.TaskID), result.State, result.Already); err != nil {
		return 1
	}
	return 0
}
