package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/supervise"
)

const supervisionHelp = "concierge supervise task TASK_ID; concierge supervise [--interval DURATION] [--restart-tolerance N] [--restart-window DURATION] [--herdr PATH] pane --pane PANE --name NAME --kind KIND [--agent-arg ARG...]"

type supervisionCommands struct {
	Task struct {
		TaskID string `arg:"" help:"Task ID with a confirmed worker binding."`
	} `cmd:"" help:"Supervise a task and deliver pending inbox notifications."`
	Interval         time.Duration `help:"Time between health checks." default:"5s"`
	RestartTolerance int           `help:"Failed checks tolerated per window before supervision stops." default:"5"`
	RestartWindow    time.Duration `help:"Window for failed monitor checks." default:"1m"`
	Herdr            string        `help:"Herdr executable path." default:"herdr" env:"TINCAN_HERDR"`
	Pane             struct {
		Pane      string   `required:"" help:"Herdr pane ID."`
		Name      string   `required:"" help:"Live agent name."`
		Kind      string   `required:"" help:"Herdr agent kind."`
		AgentArgs []string `name:"agent-arg" sep:"none" help:"Exact launch argument; repeat in launch order."`
	} `cmd:"" help:"Supervise a manual pane using ambient Herdr routing."`
}

func (command *supervisionCommands) config() (supervise.Config, error) {
	config := supervise.Config{
		Interval:         command.Interval,
		RestartTolerance: command.RestartTolerance,
		RestartWindow:    command.RestartWindow,
	}
	if config.Interval <= 0 {
		return config, errors.New("interval must be positive")
	}
	if config.RestartTolerance < 0 || uint64(config.RestartTolerance) > 1<<32-1 ||
		config.RestartWindow <= 0 {
		return config, errors.New("invalid restart tolerance or window")
	}
	return config, nil
}

func (command *supervisionCommands) runPane(ctx context.Context, output io.Writer) int {
	config, err := command.config()
	if err != nil {
		return fail(output, 2, err, supervisionHelp)
	}
	input := command.Pane
	worker := manualWorker{
		client: herdr.NewClient(command.Herdr),
		pane:   input.Pane,
		name:   input.Name,
		kind:   input.Kind,
		args:   input.AgentArgs,
	}
	if err := supervise.Run(ctx, worker, config); err != nil {
		return fail(
			output,
			1,
			err,
			supervisionHelp+"; inspect the live pane before restarting supervision",
		)
	}
	return 0
}

type manualWorker struct {
	client           herdr.Client
	pane, name, kind string
	args             []string
}

func (worker manualWorker) Observe(ctx context.Context) error {
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

func (worker manualWorker) Restart(ctx context.Context) error {
	return worker.client.StartWithArgs(ctx, worker.pane, worker.name, worker.kind, worker.args)
}
