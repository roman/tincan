package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

func (command *cli) runResolve(ctx context.Context, root string, output io.Writer) int {
	_, err := inbox.ParseRecipient(command.Recipient)
	if err != nil {
		return fail(output, 2, err, "use a valid primary --recipient name")
	}
	if command.hostExpectations.MessageRoot != "" {
		if _, err := firstmate.ParseAbsolutePath(command.hostExpectations.MessageRoot); err != nil {
			return fail(output, 2, err, "use an absolute, clean --message-root path")
		}
	}
	response, err := hostservice.Call(
		ctx,
		filepath.Join(root, "host.sock"),
		hostservice.Request{
			Operation:    hostservice.Resolve,
			Expectations: &command.hostExpectations,
			Resolve: &hostservice.ResolveRequest{
				ReplyID:  command.Backlog.Resolve.ReplyID,
				Mode:     taskstate.CompletionMode(command.Backlog.Resolve.Mode),
				ClosedOn: command.Backlog.Resolve.ClosedOn,
			},
		},
	)
	if err == nil && response.Outcome != hostservice.Completed {
		err = errors.New(response.Error)
	}
	if err != nil {
		return fail(
			output,
			1,
			err,
			resolveUsage+"; inspect the bound reply and task; retry the same reply, mode, and closing date after correcting the error",
		)
	}
	if _, err := fmt.Fprintf(output, "task: %s\nstate: %s\nalready: %t\n", quote(response.Resolution.TaskID), response.Resolution.State, response.Resolution.Already); err != nil {
		return 1
	}
	return 0
}
