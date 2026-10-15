package herdr

import (
	"context"
	"fmt"
	"strings"
)

// SessionRunner executes commands with an explicit session and no inherited
// Herdr socket, workspace, tab, or pane selection.
type SessionRunner interface {
	RunSession(context.Context, string, ...string) ([]byte, []byte, error)
}

type boundSessionRunner struct {
	session string
	runner  SessionRunner
}

func (runner boundSessionRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	return runner.runner.RunSession(ctx, runner.session, args...)
}

// ForSession returns a copy bound to a session. The original client is unchanged.
func (client Client) ForSession(session string) (Client, error) {
	if session == "" ||
		strings.Trim(
			session,
			"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._@%+-",
		) != "" {
		return Client{}, fmt.Errorf("%w: invalid Herdr session", ErrResponse)
	}
	if client.SessionRunner == nil {
		return Client{}, fmt.Errorf("%w: no session runner", ErrCommand)
	}
	client.Runner = boundSessionRunner{session: session, runner: client.SessionRunner}
	return client, nil
}
