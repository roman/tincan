// Package control provides verified lifecycle actions for Herdr agents.
package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/roman/tincan/internal/herdr"
)

var (
	ErrUnsafeState = errors.New("agent state is unsafe for control")
	ErrUnsupported = errors.New("control action unsupported for this agent")
)

type Backend interface {
	Get(context.Context, string) (herdr.Agent, error)
	SendKeys(context.Context, string, ...string) error
	WaitSettled(context.Context, string) (herdr.Agent, error)
	Start(context.Context, string, string, string) error
}

type Controller struct{ Backend Backend }

func (c Controller) Status(ctx context.Context, target string) (herdr.Agent, error) {
	return c.Backend.Get(ctx, target)
}

func (c Controller) Interrupt(ctx context.Context, target string) (herdr.Agent, error) {
	agent, err := c.Backend.Get(ctx, target)
	if err != nil {
		return herdr.Agent{}, err
	}
	if agent.Kind != "codex" {
		return herdr.Agent{}, fmt.Errorf("%w: interrupt for %s", ErrUnsupported, agent.Kind)
	}
	switch agent.Status {
	case herdr.Idle, herdr.Done:
		return agent, nil
	case herdr.Working:
		if err := c.Backend.SendKeys(ctx, target, "esc"); err != nil {
			return herdr.Agent{}, err
		}
		settled, err := c.Backend.WaitSettled(ctx, target)
		if err != nil {
			return herdr.Agent{}, fmt.Errorf("interrupt verification: %w", err)
		}
		return settled, nil
	default:
		return herdr.Agent{}, fmt.Errorf("%w: %s", ErrUnsafeState, agent.Status)
	}
}

// Exit fails closed until the adapter can prove the composer's input is empty.
func (c Controller) Exit(context.Context, string) error {
	return fmt.Errorf("%w: exit needs composer-state verification", ErrUnsupported)
}

func (c Controller) Start(ctx context.Context, paneID, name, kind string) error {
	return c.Backend.Start(ctx, paneID, name, kind)
}
