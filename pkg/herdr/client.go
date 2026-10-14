// Package herdr adapts Herdr's agent commands to typed operations.
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

type AgentState string

const (
	Idle    AgentState = "idle"
	Done    AgentState = "done"
	Working AgentState = "working"
	Blocked AgentState = "blocked"
	Unknown AgentState = "unknown"
)

// SessionReference preserves Herdr's optional native identity metadata.
// The reference alone does not establish a live binding or a fresh launch.
type SessionReference struct {
	Source string `json:"source"`
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
}

type Agent struct {
	PaneID           string
	Status           AgentState
	Kind             string
	InteractiveReady bool
	Session          *SessionReference `json:"-"`
}

var (
	ErrWorkspaceNotFound = errors.New("herdr workspace not found")
	ErrNotFound          = errors.New("herdr agent not found")
	ErrCommand           = errors.New("herdr command failed")
	ErrResponse          = errors.New("invalid herdr response")
	ErrAgentPresent      = errors.New("herdr agent already present")
)

// Runner executes the Herdr binary. Its arguments exclude the executable name.
type Runner interface {
	Run(context.Context, ...string) ([]byte, []byte, error)
}

type Client struct {
	Runner        Runner
	SessionRunner SessionRunner
	Processes     ProcessInspector
}

func NewClient(executable string) Client {
	if executable == "" {
		executable = "herdr"
	}
	runner := commandRunner{executable: executable}
	return Client{Runner: runner, SessionRunner: runner, Processes: psInspector{}}
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
	Result struct {
		Agent struct {
			PaneID           string            `json:"pane_id"`
			Status           AgentState        `json:"agent_status"`
			Kind             string            `json:"agent"`
			InteractiveReady bool              `json:"interactive_ready"`
			Session          *SessionReference `json:"agent_session"`
		} `json:"agent"`
	} `json:"result"`
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

func (c Client) Get(ctx context.Context, target string) (Agent, error) {
	if target == "" {
		return Agent{}, fmt.Errorf("%w: empty target", ErrResponse)
	}
	stdout, err := c.run(ctx, "agent", "get", target)
	if err != nil {
		return Agent{}, err
	}
	var response responseEnvelope
	if err := json.Unmarshal(stdout, &response); err != nil {
		return Agent{}, fmt.Errorf("%w: %v", ErrResponse, err)
	}
	agent := Agent{
		PaneID:           response.Result.Agent.PaneID,
		Status:           response.Result.Agent.Status,
		Kind:             response.Result.Agent.Kind,
		InteractiveReady: response.Result.Agent.InteractiveReady,
		Session:          response.Result.Agent.Session,
	}
	if agent.PaneID == "" || !validState(agent.Status) {
		return Agent{}, fmt.Errorf("%w: missing pane or state", ErrResponse)
	}
	return agent, nil
}

func validState(state AgentState) bool {
	switch state {
	case Idle, Done, Working, Blocked, Unknown:
		return true
	default:
		return false
	}
}

// Start launches only after Herdr reports no agent in the pane. Herdr verifies
// that the pane is at an interactive shell prompt before launching.
func (c Client) Start(ctx context.Context, paneID, name, kind string) error {
	return c.StartWithArgs(ctx, paneID, name, kind, nil)
}

func (c Client) StartWithArgs(
	ctx context.Context,
	paneID, name, kind string,
	agentArgs []string,
) error {
	if paneID == "" || name == "" || kind == "" {
		return fmt.Errorf("%w: missing start argument", ErrResponse)
	}
	_, err := c.Get(ctx, paneID)
	if err == nil {
		return ErrAgentPresent
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	args := []string{"agent", "start", name, "--kind", kind, "--pane", paneID}
	if len(agentArgs) > 0 {
		args = append(append(args, "--"), agentArgs...)
	}
	if _, err := c.run(ctx, args...); err != nil {
		return err
	}
	agent, err := c.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("start verification: %w", err)
	}
	// Herdr can mark Codex interactive before it can classify the screen. Its startup
	// accepts that unknown state only for Codex; other agents need a known state.
	codexInteractive := kind == "codex" && agent.Status == Unknown && agent.InteractiveReady
	if agent.PaneID != paneID || (agent.Status == Unknown && !codexInteractive) {
		return fmt.Errorf("start verification: %w", ErrResponse)
	}
	if agent.Kind != kind {
		return fmt.Errorf("start verification: %w: kind %s", ErrResponse, agent.Kind)
	}
	return nil
}

func (c Client) SendKeys(ctx context.Context, target string, keys ...string) error {
	if target == "" || len(keys) == 0 {
		return fmt.Errorf("%w: missing key target or keys", ErrResponse)
	}
	args := append([]string{"agent", "send-keys", target}, keys...)
	_, err := c.run(ctx, args...)
	return err
}

func (c Client) WaitSettled(ctx context.Context, target string) (Agent, error) {
	if target == "" {
		return Agent{}, fmt.Errorf("%w: empty target", ErrResponse)
	}
	if _, err := c.run(ctx, "agent", "wait", target, "--timeout", "10000"); err != nil {
		return Agent{}, err
	}
	agent, err := c.Get(ctx, target)
	if err != nil {
		return Agent{}, err
	}
	if agent.Status != Idle && agent.Status != Done && agent.Status != Blocked {
		return Agent{}, fmt.Errorf("%w: agent did not settle", ErrResponse)
	}
	return agent, nil
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
