package hostservice

import (
	"context"
	"errors"
	"strings"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/control"
)

const (
	ControlStart     Operation = "control_start"
	ControlInterrupt Operation = "control_interrupt"
)

type ControlRequest struct {
	Target string `json:"target"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

type ControlResult struct {
	Target string      `json:"target"`
	Agent  herdr.Agent `json:"agent"`
}

func validateControlInput(operation Operation, input *ControlRequest) error {
	if input == nil || strings.TrimSpace(input.Target) == "" {
		return errors.New("control requires a target")
	}
	if operation == ControlInterrupt {
		if input.Name != "" || input.Kind != "" {
			return errors.New("interrupt accepts only a target")
		}
		return nil
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Kind) == "" {
		return errors.New("start requires a name and kind")
	}
	return nil
}

func (server *Server) manualClient() (herdr.Client, error) {
	if server.config.Session == "" {
		return herdr.Client{}, errors.New("host needs an explicit session for manual control")
	}
	return herdr.NewClient(server.config.HerdrExecutable).ForSession(server.config.Session)
}

func (server *Server) control(
	ctx context.Context,
	operation Operation,
	input ControlRequest,
) (Response, error) {
	client, err := server.manualClient()
	if err != nil {
		return Response{}, err
	}
	endpoint := controllerEndpoint{session: server.config.Session, pane: input.Target}
	if operation == ControlInterrupt {
		agent, err := client.Get(ctx, input.Target)
		if err != nil {
			return Response{}, err
		}
		endpoint.pane = agent.PaneID
	}
	if _, err := herdr.ParseTarget(endpoint.session, endpoint.pane); err != nil {
		return Response{}, err
	}
	release, err := server.controllers.acquire(endpoint)
	if err != nil {
		return Response{}, err
	}
	defer release()
	if operation == ControlStart {
		if err := server.rejectTaskBoundPane(ctx, endpoint); err != nil {
			return Response{}, err
		}
	}
	controller := control.Controller{Backend: client}
	var agent herdr.Agent
	if operation == ControlStart {
		err = controller.Start(ctx, input.Target, input.Name, input.Kind)
		if err == nil {
			agent, err = controller.Status(ctx, input.Target)
		}
	} else {
		agent, err = controller.Interrupt(ctx, endpoint.pane)
	}
	if err != nil {
		return Response{}, err
	}
	if agent.PaneID != endpoint.pane {
		return Response{}, errors.New("control endpoint changed")
	}
	return Response{Control: &ControlResult{Target: input.Target, Agent: agent}}, nil
}

func validateControlResponse(response Response, request Request) error {
	result := response.Control
	if result == nil || response.Assignment != nil || response.Task != nil ||
		response.Transition != nil ||
		response.Resolution != nil ||
		response.Stopped != nil ||
		response.Error != "" {
		return errors.New("host returned an incomplete control result")
	}
	if result.Target != request.Control.Target || result.Agent.PaneID == "" {
		return errors.New("host returned a different control target")
	}
	agent := result.Agent
	if request.Operation == ControlInterrupt {
		if agent.Kind != "codex" || (agent.Status != herdr.Idle && agent.Status != herdr.Done) {
			return errors.New("host returned an unsettled interrupt result")
		}
		return nil
	}
	if agent.PaneID != request.Control.Target || agent.Kind != request.Control.Kind {
		return errors.New("host returned a different started agent")
	}
	switch agent.Status {
	case herdr.Idle, herdr.Done, herdr.Working, herdr.Blocked:
		return nil
	case herdr.Unknown:
		if agent.Kind == "codex" && agent.InteractiveReady {
			return nil
		}
	}
	return errors.New("host returned an unverified start result")
}
