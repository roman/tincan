package conciergecli

import (
	"context"
	"fmt"
	"io"

	"github.com/roman/tincan/pkg/control"
	"github.com/roman/tincan/pkg/herdr"
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

func (command *controlCommands) run(ctx context.Context, selected string, output io.Writer) int {
	backend := herdr.NewClient(command.Herdr)
	if command.Session != "" {
		bound, err := backend.ForSession(command.Session)
		if err != nil {
			return fail(output, 2, err, controlHelp)
		}
		backend = bound
	}
	controller := control.Controller{Backend: backend}
	var agent herdr.Agent
	var err error
	switch selected {
	case "control status <target>":
		agent, err = controller.Status(ctx, command.Status.Target)
	case "control interrupt <target>":
		agent, err = controller.Interrupt(ctx, command.Interrupt.Target)
	case "control start":
		input := command.Start
		err = controller.Start(ctx, input.Pane, input.Name, input.Kind)
		if err == nil {
			agent, err = controller.Status(ctx, input.Pane)
		}
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), controlHelp)
	}
	if err != nil {
		return fail(output, 1, err, controlHelp+"; inspect the live agent before retrying")
	}
	if _, err := fmt.Fprintf(output, "pane_id: %s\nstatus: %s\n", quote(agent.PaneID), agent.Status); err != nil {
		return 1
	}
	return 0
}
