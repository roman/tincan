package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskops"
)

func readBodyFile(path string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	return string(data), err
}

func (command *taskCommands) sendAssignment(
	ctx context.Context,
	root string,
	expectations hostservice.Expectations,
	stdin io.Reader,
	output io.Writer,
) int {
	body, err := readBodyFile(command.Send.BodyFile, stdin)
	if err != nil {
		return fail(output, 1, err, "provide a readable --body-file or use --body-file - for stdin")
	}
	response, err := hostservice.Call(ctx, filepath.Join(root, "host.sock"), hostservice.Request{
		Operation: hostservice.SendAssignment, Expectations: &expectations,
		Assignment: &hostservice.AssignmentRequest{
			TaskID: command.Send.TaskID,
			Key:    command.Send.Key,
			Body:   body,
		},
	})
	if err == nil && response.Outcome != hostservice.Completed {
		err = errors.New(response.Error)
	}
	result := response.Assignment

	if result != nil {
		if _, writeErr := fmt.Fprintf(output, "message_id: %s\nnotification: %s\n", quote(result.MessageID), result.Notification); writeErr != nil {
			return 1
		}
	}
	if err != nil {
		code := 1
		if response.Outcome == hostservice.Invalid {
			code = 2
		}
		return fail(
			output,
			code,
			err,
			"inspect the task and assignment mailbox before sending more work",
		)
	}
	if result != nil && result.Notification == taskops.NotificationFailed {
		notificationErr := errors.New(result.NotificationError)
		help := "assignment is durable; inspect the pending message and worker before sending more work"
		if command.Send.Key != "" {
			help = "assignment is durable; inspect the pending message and worker; retry the same keyed command only while its generation is unchanged"
		}
		return fail(output, 1, notificationErr, help)
	}
	return 0
}
