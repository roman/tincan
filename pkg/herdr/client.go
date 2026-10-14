// Package herdr sends Tincan doorbells through a selected Herdr session.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	ErrWorkspaceNotFound = errors.New("herdr workspace not found")
	ErrNotFound          = errors.New("herdr agent not found")
	ErrCommand           = errors.New("herdr command failed")
	ErrResponse          = errors.New("invalid herdr response")
	ErrAgentNotReady     = errors.New("herdr agent not ready")
)

// Runner executes the Herdr binary. Its arguments exclude the executable name.
type Runner interface {
	Run(context.Context, ...string) ([]byte, []byte, error)
}

type Client struct {
	Runner        Runner
	SessionRunner SessionRunner
}

func NewClient(executable string) Client {
	if executable == "" {
		executable = "herdr"
	}
	runner := commandRunner{executable: executable}
	return Client{Runner: runner, SessionRunner: runner}
}

type commandRunner struct{ executable string }

func (r commandRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, r.executable, args...)
	stdout, err := command.Output()
	if err == nil {
		return stdout, nil, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return stdout, exitError.Stderr, err
	}
	return stdout, nil, err
}

func (r commandRunner) RunSession(
	ctx context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, r.executable, args...)
	var environment []string
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		switch name {
		case "HERDR_SESSION",
			"HERDR_SOCKET_PATH",
			"HERDR_WORKSPACE_ID",
			"HERDR_TAB_ID",
			"HERDR_PANE_ID":
			continue
		}
		environment = append(environment, variable)
	}
	command.Env = append(environment, "HERDR_SESSION="+session)
	stdout, err := command.Output()
	if err == nil {
		return stdout, nil, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return stdout, exitError.Stderr, err
	}
	return stdout, nil, err
}

type responseEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Runner == nil {
		return nil, fmt.Errorf("%w: no runner", ErrCommand)
	}
	stdout, stderr, err := c.Runner.Run(ctx, args...)
	if err == nil {
		return stdout, nil
	}
	for _, output := range [][]byte{stdout, stderr} {
		var response responseEnvelope
		if json.Unmarshal(output, &response) != nil || response.Error.Code == "" {
			continue
		}
		if response.Error.Code == "workspace_not_found" {
			return nil, fmt.Errorf(
				"%w: %w: %s",
				ErrCommand,
				ErrWorkspaceNotFound,
				response.Error.Message,
			)
		}
		if response.Error.Code == "agent_not_ready" {
			return nil, fmt.Errorf(
				"%w: %w: %s",
				ErrCommand,
				ErrAgentNotReady,
				response.Error.Message,
			)
		}
		if response.Error.Code == "agent_not_found" {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, response.Error.Message)
		}
		if response.Error.Message != "" {
			return nil, fmt.Errorf("%w: %s", ErrCommand, response.Error.Message)
		}
		return nil, fmt.Errorf("%w: %s", ErrCommand, response.Error.Code)
	}
	detail := strings.TrimSpace(string(stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(stdout))
	}
	if detail == "" {
		detail = err.Error()
	}
	return nil, fmt.Errorf("%w: %s", ErrCommand, detail)
}

// Prompt submits a short notification. Submission does not confirm that the
// agent read it; callers must use their own acknowledgement record.
func (c Client) Prompt(ctx context.Context, target, message string) error {
	if target == "" || message == "" {
		return fmt.Errorf("%w: empty prompt target or message", ErrResponse)
	}
	_, err := c.run(ctx, "agent", "prompt", target, message)
	return err
}
