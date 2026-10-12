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
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/resident"
	"github.com/roman/tincan/pkg/stateversion"
	"github.com/roman/tincan/pkg/supervise"
)

type cli struct {
	Root             string        `help:"Absolute Concierge state root for a registered task."`
	MessageRoot      string        `help:"Absolute Tincan message root for task bootstrap messages."`
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
		fmt.Fprintln(os.Stderr, "concierge-supervise:", err)
		os.Exit(2)
	}
	if args.Task != "" {
		versionGuard, err := stateversion.AcquireLegacy(ctx, args.Root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "concierge-supervise:", err)
			os.Exit(1)
		}
		defer versionGuard.Close()
	}
	config := supervise.Config{
		Interval:         args.Interval,
		RestartTolerance: args.RestartTolerance,
		RestartWindow:    args.RestartWindow,
	}
	var runErr error
	if taskWorker, ok := worker.(registeredWorker); ok {
		runErr = runResidentTask(ctx, taskWorker, config)
	} else {
		runErr = supervise.Run(ctx, worker, config)
	}
	if err := runErr; err != nil {
		fmt.Fprintln(os.Stderr, "concierge-supervise:", err)
		os.Exit(1)
	}
}

func runResidentTask(ctx context.Context, worker registeredWorker, config supervise.Config) error {
	lease, err := resident.AcquireTaskLease(worker.taskRoot, worker.taskID)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err := supervise.Check(ctx, worker); err != nil && !errors.Is(err, supervise.ErrRestarted) {
		return err
	}
	record, err := worker.registry.Get(worker.taskID)
	if err != nil {
		return err
	}
	client, err := worker.client.ForSession(record.HerdrSession)
	if err != nil {
		return err
	}
	store, err := inbox.New(worker.messageRoot)
	if err != nil {
		return err
	}
	recipient, err := inbox.ParseRecipient(worker.taskID)
	if err != nil {
		return err
	}
	controller := resident.Controller{
		Watcher: resident.Watcher{
			Store: store, Recipient: recipient, Target: record.PaneID, Doorbell: client,
		},
		Worker: worker, Supervision: config,
	}
	return controller.Run(ctx, nil)
}
