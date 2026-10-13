package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/roman/tincan/pkg/hostservice"
)

func (command *cli) runSupervision(
	ctx context.Context,
	root, selected string,
	output io.Writer,
) int {
	if _, err := command.Supervise.config(); err != nil {
		return fail(output, 2, err, supervisionHelp)
	}
	input := &hostservice.SupervisionRequest{}
	operation := hostservice.Supervise
	switch selected {
	case "supervise task <task-id>":
		input.TaskID = command.Supervise.Task.TaskID
	case "supervise pane":
		pane := command.Supervise.Pane
		input.Pane = &hostservice.PaneSupervision{
			Pane:      pane.Pane,
			Name:      pane.Name,
			Kind:      pane.Kind,
			AgentArgs: pane.AgentArgs,
		}
	case "supervise stop task <task-id>":
		operation = hostservice.StopSupervision
		input.TaskID = command.Supervise.Stop.Task.TaskID
	case "supervise stop pane":
		operation = hostservice.StopSupervision
		input.Pane = &hostservice.PaneSupervision{Pane: command.Supervise.Stop.Pane.Pane}
	}
	if operation == hostservice.Supervise {
		input.Settings = command.supervisionExpectations
	} else if settings := command.supervisionExpectations; settings != nil && (settings.Interval != nil || settings.RestartTolerance != nil || settings.RestartWindow != nil) {
		return fail(output, 2, errors.New("stopping supervision forbids monitor settings"), supervisionHelp)
	}
	response, err := hostservice.Call(
		ctx,
		filepath.Join(root, "host.sock"),
		hostservice.Request{
			Operation:    operation,
			Expectations: &command.hostExpectations,
			Supervision:  input,
		},
	)
	if err != nil {
		return fail(output, 1, err, supervisionHelp+"; inspect host diagnostics before retrying")
	}
	if response.Outcome != hostservice.Completed {
		code := 1
		if response.Outcome == hostservice.Invalid {
			code = 2
		}
		return fail(output, code, errors.New(response.Error), supervisionHelp)
	}
	result := response.Supervision
	identity, value := "task", result.TaskID
	if result.PaneID != "" {
		identity, value = "pane", result.PaneID
	}
	if _, err := fmt.Fprintf(output, "%s: %s\nsupervision: %s\n", identity, quote(value), result.State); err != nil {
		return 1
	}
	return 0
}
