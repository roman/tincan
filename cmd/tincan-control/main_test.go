package main

import (
	"context"
	"errors"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/herdr"
)

type sessionProbe struct {
	session string
	calls   int
}

func (probe *sessionProbe) Run(context.Context, ...string) ([]byte, []byte, error) {
	return nil, nil, errors.New("used caller session")
}

func (probe *sessionProbe) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	probe.session = session
	probe.calls++
	if len(args) != 3 || args[0] != "agent" || args[1] != "get" || args[2] != "pane-1" {
		return nil, nil, errors.New("unexpected command")
	}
	return []byte(
		`{"result":{"agent":{"pane_id":"pane-1","agent_status":"idle","agent":"codex"}}}`,
	), nil, nil
}

func TestExplicitSessionRoutesStatus(t *testing.T) {
	var options cli
	parser, err := kong.New(&options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{"--session", "crew", "status", "pane-1"}); err != nil {
		t.Fatal(err)
	}
	probe := &sessionProbe{}
	controller, err := controllerFor(options, herdr.Client{Runner: probe, SessionRunner: probe})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := controller.Status(context.Background(), options.Status.Target)
	if err != nil || agent.PaneID != "pane-1" || probe.session != "crew" || probe.calls != 1 {
		t.Fatalf("agent=%+v session=%q calls=%d err=%v", agent, probe.session, probe.calls, err)
	}
}
