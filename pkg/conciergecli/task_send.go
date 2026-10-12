package conciergecli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/roman/tincan/pkg/herdr"
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
	service *taskops.Service,
	messageRoot string,
	stdin io.Reader,
	output io.Writer,
) int {
	body, err := readBodyFile(command.Send.BodyFile, stdin)
	if err != nil {
		return fail(output, 1, err, "provide a readable --body-file or use --body-file - for stdin")
	}
	result, err := service.SendAssignment(
		ctx,
		messageRoot,
		command.Send.TaskID,
		command.Send.Key,
		body,
		herdr.NewClient("herdr"),
	)
	if result.Message.ID.String() != "" {
		if _, writeErr := fmt.Fprintf(output, "message_id: %s\nnotification: %s\n", quote(result.Message.ID.String()), result.Notification); writeErr != nil {
			return 1
		}
	}
	if err != nil {
		return fail(
			output,
			1,
			err,
			"inspect the task and assignment mailbox before sending more work",
		)
	}
	if result.Notification == taskops.NotificationFailed {
		notificationErr := result.NotificationError
		if notificationErr == nil {
			notificationErr = fmt.Errorf("worker notification failed")
		}
		help := "assignment is durable; inspect the pending message and worker before sending more work"
		if command.Send.Key != "" {
			help = "assignment is durable; retry the same keyed command after correcting the notification error"
		}
		return fail(output, 1, notificationErr, help)
	}
	return 0
}
