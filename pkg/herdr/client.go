// Package herdr provides command execution, session binding, and notifications
// for Herdr. Applications can build typed operations on Client.Run.
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

// ResponseEnvelope holds Herdr's common JSON wrapper without prescribing the
// application-specific result schema.
type ResponseEnvelope[T any] struct {
	Result T             `json:"result"`
	Error  ResponseError `json:"error"`
}

// ResponseError describes an error returned by Herdr.
type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (response ResponseError) commandError() error {
	detail := response.Message
	if detail == "" {
		detail = response.Code
	}
	switch response.Code {
	case "workspace_not_found":
		return fmt.Errorf("%w: %w: %s", ErrCommand, ErrWorkspaceNotFound, detail)
	case "agent_not_ready":
		return fmt.Errorf("%w: %w: %s", ErrCommand, ErrAgentNotReady, detail)
	case "agent_not_found":
		return fmt.Errorf("%w: %s", ErrNotFound, detail)
	default:
		return fmt.Errorf("%w: %s", ErrCommand, detail)
	}
}

// DecodeResponse decodes a command's result and maps Herdr errors to the same
// sentinel errors as Client.Run. Callers validate fields required by their operation.
func DecodeResponse[T any](data []byte) (T, error) {
	var result T
	var response ResponseEnvelope[json.RawMessage]
	if err := json.Unmarshal(data, &response); err != nil {
		return result, fmt.Errorf("%w: %v", ErrResponse, err)
	}
	if response.Error.Code != "" {
		return result, response.Error.commandError()
	}
	if len(response.Result) == 0 || string(response.Result) == "null" {
		return result, fmt.Errorf("%w: missing result", ErrResponse)
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return result, fmt.Errorf("%w: %v", ErrResponse, err)
	}
	return result, nil
}

// Run executes arbitrary Herdr subcommands and returns stdout. Arguments exclude
// the executable name. A client returned by ForSession uses that session.
// Command failures preserve Herdr's sentinel errors; successful output remains
// available for DecodeResponse or an application-specific decoder.
func (c Client) Run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Runner == nil {
		return nil, fmt.Errorf("%w: no runner", ErrCommand)
	}
	stdout, stderr, err := c.Runner.Run(ctx, args...)
	if err == nil {
		return stdout, nil
	}
	for _, output := range [][]byte{stdout, stderr} {
		var response ResponseEnvelope[json.RawMessage]
		if json.Unmarshal(output, &response) != nil || response.Error.Code == "" {
			continue
		}
		return nil, response.Error.commandError()
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
	_, err := c.Run(ctx, "agent", "prompt", target, message)
	return err
}
