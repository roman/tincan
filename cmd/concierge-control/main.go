package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/control"
	"github.com/roman/tincan/pkg/herdr"
)

type cli struct {
	Herdr     string           `help:"Herdr executable path." default:"herdr" env:"TINCAN_HERDR"`
	Session   string           `help:"Named Herdr session to control."`
	Status    statusCommand    `cmd:"" help:"Read an agent's observed state."`
	Interrupt interruptCommand `cmd:"" help:"Interrupt a working agent and wait for a settled state."`
	Start     startCommand     `cmd:"" help:"Start an agent in an empty shell pane."`
}

type statusCommand struct {
	Target string `arg:"" help:"Live agent name or pane ID."`
}
type interruptCommand struct {
	Target string `arg:"" help:"Live agent name or pane ID."`
}
type startCommand struct {
	Pane string `required:"" help:"Existing pane ID at an interactive shell prompt."`
	Name string `required:"" help:"Unique live agent name."`
	Kind string `required:"" help:"Herdr-supported agent kind."`
}

type environment struct {
	controller control.Controller
	context    context.Context
}

func (command *statusCommand) Run(env *environment) error {
	agent, err := env.controller.Status(env.context, command.Target)
	if err != nil {
		return err
	}
	printAgent(agent)
	return nil
}

func (command *interruptCommand) Run(env *environment) error {
	agent, err := env.controller.Interrupt(env.context, command.Target)
	if err != nil {
		return err
	}
	printAgent(agent)
	return nil
}

func (command *startCommand) Run(env *environment) error {
	if err := env.controller.Start(env.context, command.Pane, command.Name, command.Kind); err != nil {
		return err
	}
	agent, err := env.controller.Status(env.context, command.Pane)
	if err != nil {
		return err
	}
	printAgent(agent)
	return nil
}

func printAgent(agent herdr.Agent) {
	fmt.Printf("pane_id: %s\nstatus: %s\n", agent.PaneID, agent.Status)
}

func controllerFor(options cli, backend herdr.Client) (control.Controller, error) {
	if options.Session != "" {
		bound, err := backend.ForSession(options.Session)
		if err != nil {
			return control.Controller{}, err
		}
		backend = bound
	}
	return control.Controller{Backend: backend}, nil
}

func main() {
	var options cli
	command := kong.Parse(
		&options,
		kong.Name("concierge-control"),
		kong.Description("Control Herdr-hosted agents."),
	)
	backend := herdr.NewClient(options.Herdr)
	controller, err := controllerFor(options, backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	env := &environment{
		controller: controller,
		context:    context.Background(),
	}
	if err := command.Run(env); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
