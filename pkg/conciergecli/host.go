package conciergecli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
)

const hostHelp = "concierge --root ROOT host serve; initialize the state root first and run the host outside the model sandbox"

func (command *cli) runHost(ctx context.Context, root string, output io.Writer) int {
	recipient, err := inbox.ParseRecipient(command.Recipient)
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	server, err := hostservice.New(root, recipient)
	if err != nil {
		return fail(output, 2, err, hostHelp)
	}
	listener, err := server.Listen(ctx)
	if err != nil {
		return fail(output, 1, err, hostHelp)
	}
	defer listener.Close()
	if _, err := fmt.Fprintf(output, "status: ready\nsocket: %s\n", quote(filepath.Join(root, "host.sock"))); err != nil {
		return 1
	}
	if err := server.Serve(ctx, listener.UnixListener); err != nil {
		return fail(output, 1, err, hostHelp)
	}
	return 0
}
