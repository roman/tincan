package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/supervise"
)

type cli struct {
	Root             string        `help:"Absolute Tincan state root for a registered task."`
	Task             string        `help:"Registered local task ID to supervise."`
	Pane             string        `help:"Herdr pane ID for manual supervision."`
	Name             string        `help:"Agent name for manual supervision."`
	Kind             string        `help:"Herdr agent kind for manual supervision."`
	AgentArgs        []string      `name:"agent-arg" sep:"none" help:"Agent launch argument; repeat to preserve model, effort, or resume identity."`
	Interval         time.Duration `help:"Time between health checks." default:"5s"`
	RestartTolerance int           `help:"Failed checks tolerated per window; the next failure stops supervision." default:"5"`
	RestartWindow    time.Duration `help:"Window for failed monitor checks." default:"1m"`
	Herdr            string        `help:"Herdr executable path." default:"herdr"`
}

type herdrWorker struct {
	client herdr.Client
	pane   string
	name   string
	kind   string
	args   []string
}

func (worker herdrWorker) Observe(ctx context.Context) error {
	agent, err := worker.client.Get(ctx, worker.name)
	if errors.Is(err, herdr.ErrNotFound) {
		return supervise.ErrMissing
	}
	if err != nil {
		return err
	}
	if agent.PaneID != worker.pane || agent.Kind != worker.kind {
		return fmt.Errorf("unexpected agent in pane %s", worker.pane)
	}
	return nil
}

func (worker herdrWorker) Restart(ctx context.Context) error {
	return worker.client.StartWithArgs(ctx, worker.pane, worker.name, worker.kind, worker.args)
}

func main() {
	var args cli
	kong.Parse(&args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	worker, err := workerFromCLI(args, herdr.NewClient(args.Herdr))
	if err != nil {
		fmt.Fprintln(os.Stderr, "tincan-supervise:", err)
		os.Exit(2)
	}
	config := supervise.Config{
		Interval:         args.Interval,
		RestartTolerance: args.RestartTolerance,
		RestartWindow:    args.RestartWindow,
	}
	if err := supervise.Run(ctx, worker, config); err != nil {
		fmt.Fprintln(os.Stderr, "tincan-supervise:", err)
		os.Exit(1)
	}
}
