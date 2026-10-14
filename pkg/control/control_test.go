package control

import (
	"context"
	"errors"
	"testing"

	"github.com/roman/tincan/internal/herdr"
)

type fakeBackend struct {
	agents  []herdr.Agent
	errors  []error
	keys    []string
	wait    herdr.Agent
	waitErr error
	started bool
}

func (b *fakeBackend) Get(context.Context, string) (herdr.Agent, error) {
	agent, err := b.agents[0], b.errors[0]
	b.agents, b.errors = b.agents[1:], b.errors[1:]
	return agent, err
}
func (b *fakeBackend) SendKeys(_ context.Context, _ string, keys ...string) error {
	b.keys = append(b.keys, keys...)
	return nil
}
func (b *fakeBackend) WaitSettled(context.Context, string) (herdr.Agent, error) {
	return b.wait, b.waitErr
}
func (b *fakeBackend) Start(context.Context, string, string, string) error {
	b.started = true
	return nil
}

func TestInterrupt(t *testing.T) {
	tests := []struct {
		name      string
		state     herdr.AgentState
		wantKey   string
		wantError error
	}{
		{name: "working", state: herdr.Working, wantKey: "esc"},
		{name: "idle", state: herdr.Idle},
		{name: "unknown", state: herdr.Unknown, wantError: ErrUnsafeState},
		{name: "blocked", state: herdr.Blocked, wantError: ErrUnsafeState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeBackend{
				agents: []herdr.Agent{{PaneID: "w1:p2", Status: tt.state, Kind: "codex"}},
				errors: []error{nil},
				wait:   herdr.Agent{PaneID: "w1:p2", Status: herdr.Idle},
			}
			_, err := (Controller{Backend: backend}).Interrupt(context.Background(), "worker")
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("error = %v, want %v", err, tt.wantError)
			}
			if len(backend.keys) > 0 && backend.keys[0] != tt.wantKey {
				t.Fatalf("keys = %v", backend.keys)
			}
			if tt.wantKey == "" && len(backend.keys) > 0 {
				t.Fatalf("unexpected keys: %v", backend.keys)
			}
		})
	}
}

func TestExitFailsClosed(t *testing.T) {
	backend := &fakeBackend{
		agents: []herdr.Agent{{PaneID: "w1:p2", Status: herdr.Idle, Kind: "codex"}},
		errors: []error{nil},
	}
	err := (Controller{Backend: backend}).Exit(context.Background(), "worker")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
	if len(backend.keys) > 0 {
		t.Fatalf("unexpected keys: %v", backend.keys)
	}
}

func TestExitRefusesUnknown(t *testing.T) {
	backend := &fakeBackend{
		agents: []herdr.Agent{{PaneID: "w1:p2", Status: herdr.Unknown}},
		errors: []error{nil},
	}
	err := (Controller{Backend: backend}).Exit(context.Background(), "worker")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
	if len(backend.keys) > 0 {
		t.Fatalf("unexpected keys: %v", backend.keys)
	}
}

func TestInterruptRefusesUnknownKind(t *testing.T) {
	backend := &fakeBackend{
		agents: []herdr.Agent{{PaneID: "w1:p2", Status: herdr.Working, Kind: "mystery"}},
		errors: []error{nil},
	}
	_, err := (Controller{Backend: backend}).Interrupt(context.Background(), "worker")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
	if len(backend.keys) > 0 {
		t.Fatalf("unexpected keys: %v", backend.keys)
	}
}
