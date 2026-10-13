package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
)

func (command *cli) runHostStatus(ctx context.Context, root string, output io.Writer) int {
	if command.Host.Status.Timeout <= 0 {
		return fail(output, 2, errors.New("status timeout must be positive"), "use --timeout 1s")
	}
	messageRoot, err := rootWithDefault(command.MessageRoot, ".tincan")
	if err != nil {
		return fail(output, 2, err, "use an absolute message root")
	}
	if _, err := inbox.ParseRecipient(command.Recipient); err != nil {
		return fail(output, 2, err, "use a valid primary recipient")
	}
	executable, err := os.Executable()
	if err != nil {
		return fail(output, 2, err, "inspect the installed Concierge executable")
	}
	expected := command.hostExpectations
	expected.MessageRoot, expected.Recipient, expected.Session = messageRoot, command.Recipient, command.Host.Session
	if os.Getenv("TINCAN_HERDR") != "" {
		expected.HerdrExecutable, err = resolveHerdrExecutable(command.Host.Herdr)
		if err != nil {
			return fail(output, 2, err, "inspect the explicit Herdr configuration")
		}
	}
	probe, cancel := context.WithTimeout(ctx, command.Host.Status.Timeout)
	defer cancel()
	response, err := hostservice.Call(
		probe,
		filepath.Join(root, "host.sock"),
		hostservice.Request{
			Operation:    hostservice.HostStatus,
			Status:       &hostservice.StatusRequest{},
			Expectations: &expected,
		},
	)
	if err != nil {
		state, code := "error", 2
		switch {
		case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
			state, code = "unavailable", 1
		case errors.Is(probe.Err(), context.DeadlineExceeded):
			state, code = "unresponsive", 3
		}
		fmt.Fprintf(output, "status: %s\n", state)
		return fail(output, code, err, "inspect the host before starting or replacing it")
	}
	if response.Outcome != hostservice.Completed {
		fmt.Fprintln(output, "status: incompatible")
		return fail(
			output,
			2,
			errors.New(response.Error),
			"inspect the existing host configuration and package",
		)
	}
	status := response.Status
	if status.Root != root || status.MessageRoot != messageRoot ||
		status.Recipient != command.Recipient ||
		(command.Host.Session != "" && status.Session != command.Host.Session) ||
		status.Executable != executable {
		fmt.Fprintln(output, "status: incompatible")
		return fail(
			output,
			2,
			errors.New("running host package or configuration does not match this command"),
			"inspect and stop the existing host before replacing it",
		)
	}
	_, err = fmt.Fprintf(
		output,
		"status: ready\nroot: %s\nmessage_root: %s\nrecipient: %s\nsession: %s\nexecutable: %s\n",
		quote(status.Root),
		quote(status.MessageRoot),
		quote(status.Recipient),
		quote(status.Session),
		quote(status.Executable),
	)
	if err != nil {
		return 1
	}
	return 0
}
