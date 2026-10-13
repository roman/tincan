package conciergecli

import (
	"errors"
	"time"

	"github.com/roman/tincan/pkg/supervise"
)

const supervisionHelp = "concierge supervise task TASK_ID; concierge supervise [--interval DURATION] [--restart-tolerance N] [--restart-window DURATION] [--herdr PATH] pane --pane PANE --name NAME --kind KIND [--agent-arg ARG...]; concierge supervise stop task TASK_ID; concierge supervise stop pane --pane PANE"

type supervisionCommands struct {
	Task struct {
		TaskID string `arg:"" help:"Task ID with a confirmed worker binding."`
	} `cmd:"" help:"Supervise a task and deliver pending inbox notifications."`
	Stop struct {
		Task struct {
			TaskID string `arg:"" help:"Task ID."`
		} `cmd:"" help:"Stop a task resident without stopping its worker."`
		Pane struct {
			Pane string `required:"" help:"Herdr pane ID."`
		} `cmd:"" help:"Stop a manual pane resident."`
	} `cmd:"" help:"Stop host-owned supervision."`
	Session          string        `help:"Expected host session for manual panes." env:"HERDR_SESSION"`
	Interval         time.Duration `help:"Time between health checks." default:"5s"`
	RestartTolerance int           `help:"Failed checks tolerated per window before supervision stops." default:"5"`
	RestartWindow    time.Duration `help:"Window for failed monitor checks." default:"1m"`
	Herdr            string        `help:"Herdr executable path." default:"herdr" env:"TINCAN_HERDR"`
	Pane             struct {
		Pane      string   `required:"" help:"Herdr pane ID."`
		Name      string   `required:"" help:"Live agent name."`
		Kind      string   `required:"" help:"Herdr agent kind."`
		AgentArgs []string `name:"agent-arg" sep:"none" help:"Exact launch argument; repeat in launch order."`
	} `cmd:"" help:"Supervise a manual pane using the host session."`
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
