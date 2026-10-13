package conciergecli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskops"
)

const hostHelp = "concierge --root ROOT host serve; initialize the state root first and run the host outside the model sandbox"

func (command *cli) runHost(ctx context.Context, root string, output io.Writer) int {
	monitor, err := (&supervisionCommands{Interval: command.Host.Interval, RestartTolerance: command.Host.RestartTolerance, RestartWindow: command.Host.RestartWindow}).config()
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	recipient, err := inbox.ParseRecipient(command.Recipient)
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	messageRoot, err := rootWithDefault(command.MessageRoot, ".tincan")
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	executable, err := resolveHerdrExecutable(command.Host.Herdr)
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	conciergeExecutable, err := os.Executable()
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	tincanExecutable, err := exec.LookPath("tincan")
	if err != nil {
		return fail(output, 2, err, "install Tincan beside Concierge before starting the host")
	}
	tincanExecutable, err = filepath.Abs(tincanExecutable)
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	gitExecutable, err := exec.LookPath("git")
	if err != nil {
		return fail(output, 2, err, "install Git before starting the host")
	}
	gitExecutable, err = filepath.Abs(gitExecutable)
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	server, err := hostservice.New(
		hostservice.Config{
			Root:            root,
			MessageRoot:     messageRoot,
			Recipient:       recipient,
			HerdrExecutable: executable,
			GitExecutable:   gitExecutable,
			WorkerCommands: taskops.CommandPaths{
				Concierge: conciergeExecutable,
				Tincan:    tincanExecutable,
			},
			Session:     command.Host.Session,
			Supervision: monitor,
		},
	)
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	listener, err := server.Listen(ctx)
	if err != nil {
		return fail(output, 1, err, hostHelp)
	}
	defer listener.Close()
	if err := server.ServeReady(ctx, listener.UnixListener, func() error {
		_, err := fmt.Fprintf(output, "status: ready\nsocket: %s\n", quote(filepath.Join(root, "host.sock")))
		return err
	}); err != nil {
		return fail(output, 1, err, hostHelp)
	}

	return 0
}
