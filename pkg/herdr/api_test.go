package herdr_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
)

type sessionProbe struct {
	sessions []string
	args     [][]string
}

func (probe *sessionProbe) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	probe.sessions = append(probe.sessions, session)
	probe.args = append(probe.args, args)
	return []byte(`{"result":{"pane_id":"p1"}}`), nil, nil
}

func TestExtensionRunsCommandsInBoundSession(t *testing.T) {
	probe := &sessionProbe{}
	base := herdr.Client{SessionRunner: probe}
	bound, err := base.ForSession("crew")
	if err != nil {
		t.Fatal(err)
	}
	data, err := bound.Run(context.Background(), "pane", "get", "p1")
	if err != nil {
		t.Fatal(err)
	}
	result, err := herdr.DecodeResponse[struct {
		PaneID string `json:"pane_id"`
	}](data)
	if err != nil || result.PaneID != "p1" {
		t.Fatalf("result = %v, error = %v", result, err)
	}
	if !reflect.DeepEqual(probe.sessions, []string{"crew"}) ||
		!reflect.DeepEqual(probe.args, [][]string{{"pane", "get", "p1"}}) {
		t.Fatalf("sessions = %v, args = %v", probe.sessions, probe.args)
	}
	if _, err := base.Run(context.Background(), "pane", "list"); !errors.Is(err, herdr.ErrCommand) {
		t.Fatalf("unbound client changed: %v", err)
	}
}

func TestDecodeResponse(t *testing.T) {
	for _, trial := range []struct {
		name string
		data string
		want error
	}{
		{"result", `{"result":{}}`, nil},
		{"malformed", `{`, herdr.ErrResponse},
		{"missing result", `{}`, herdr.ErrResponse},
		{"null result", `{"result":null}`, herdr.ErrResponse},
		{"wrong result type", `{"result":[]}`, herdr.ErrResponse},
		{"missing agent", `{"error":{"code":"agent_not_found","message":"gone"}}`, herdr.ErrNotFound},
		{"missing workspace", `{"error":{"code":"workspace_not_found"}}`, herdr.ErrWorkspaceNotFound},
		{"not ready", `{"error":{"code":"agent_not_ready"}}`, herdr.ErrAgentNotReady},
		{"other error", `{"error":{"code":"server_unavailable"}}`, herdr.ErrCommand},
	} {
		t.Run(trial.name, func(t *testing.T) {
			_, err := herdr.DecodeResponse[struct{}]([]byte(trial.data))
			if !errors.Is(err, trial.want) {
				t.Fatalf("error = %v, want %v", err, trial.want)
			}
		})
	}
}
