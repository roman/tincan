package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/control"
	"github.com/roman/tincan/pkg/hostservice"
)

const controlHelp = "concierge control [--session SESSION] [--herdr PATH] status|interrupt TARGET; concierge control start --pane PANE --name NAME --kind KIND"

type controlTarget struct {
	Target string `arg:"" help:"Live agent name or pane ID."`
}

type controlCommands struct {
	Herdr     string        `help:"Herdr executable path." default:"herdr" env:"TINCAN_HERDR"`
	Session   string        `help:"Explicit Herdr session; omit to preserve ambient routing."`
	Status    controlTarget `cmd:"" help:"Read an agent's observed state."`
	Interrupt controlTarget `cmd:"" help:"Interrupt a Codex agent and wait for a settled state."`
	Start     struct {
		Pane string `required:"" help:"Existing pane ID at an interactive shell prompt."`
		Name string `required:"" help:"Unique live agent name."`
		Kind string `required:"" help:"Herdr-supported agent kind."`
	} `cmd:"" help:"Start an agent and verify its observed state."`
}

func (command *controlCommands) runStatus(ctx context.Context, output io.Writer) int {
	backend := herdr.NewClient(command.Herdr)
	if command.Session != "" {
		bound, err := backend.ForSession(command.Session)
		if err != nil {
			return fail(output, 2, err, controlHelp)
		}
		backend = bound
	}
	controller := control.Controller{Backend: backend}
	agent, err := controller.Status(ctx, command.Status.Target)
	if err != nil {
		return fail(output, 1, err, controlHelp+"; inspect the live agent before retrying")
	}
	if _, err := fmt.Fprintf(output, "pane_id: %s\nstatus: %s\n", quote(agent.PaneID), agent.Status); err != nil {
		return 1
	}
	return 0
}

func (command *controlCommands) runMutation(
	ctx context.Context,
	root, selected string,
	expectations hostservice.Expectations,
	output io.Writer,
) int {
	request := hostservice.Request{
		Expectations: &expectations,
		Control:      &hostservice.ControlRequest{},
	}
	switch selected {
	case "control start":
		request.Operation = hostservice.ControlStart
		request.Control.Target = command.Start.Pane
		request.Control.Name = command.Start.Name
		request.Control.Kind = command.Start.Kind
	case "control interrupt <target>":
		request.Operation = hostservice.ControlInterrupt
		request.Control.Target = command.Interrupt.Target
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), controlHelp)
	}
	response, err := hostservice.Call(ctx, filepath.Join(root, "host.sock"), request)
	if err == nil && response.Outcome != hostservice.Completed {
		err = errors.New(response.Error)
	}
	if err != nil {
		code := 1
		if response.Outcome == hostservice.Invalid {
			code = 2
		}
		return fail(output, code, err, controlHelp+"; inspect the live agent before retrying")
	}
	agent := response.Control.Agent
	if _, err := fmt.Fprintf(output, "pane_id: %s\nstatus: %s\n", quote(agent.PaneID), agent.Status); err != nil {
		return 1
	}
	return 0
}
