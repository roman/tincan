// Package conciergecli implements record-backed local task supervision commands.
package conciergecli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/primary"
	"github.com/roman/tincan/pkg/taskops"
)

const version = "0.1.0"

const cliHelp = "commands: host serve|status, launch TASK_ID, state init|backup|migrate|recover, brief [--limit N], primary claim|verify|release, control status|interrupt|start, supervise pane|task|stop, backlog, worker list|show, task list|pending|send|receive|ack|reply|adopt|stop|cancel|abandon; global flags: --root, --message-root, --recipient, --help; run concierge COMMAND --help for arguments"

type cli struct {
	hostExpectations        hostservice.Expectations
	supervisionExpectations *hostservice.SupervisionSettings
	Host                    struct {
		Herdr            string        `help:"Host Herdr executable." default:"herdr" env:"TINCAN_HERDR"`
		Session          string        `help:"Host session for manual pane control." env:"HERDR_SESSION"`
		Interval         time.Duration `help:"Host time between health checks." default:"5s"`
		RestartTolerance int           `help:"Host failed checks tolerated per window." default:"5"`
		RestartWindow    time.Duration `help:"Host failed-check window." default:"1m"`
		Serve            struct{}      `cmd:"" help:"Serve primary-owned operations until interrupted; run outside the model sandbox."`
		Status           struct {
			Timeout time.Duration `default:"1s" help:"Maximum time for the readiness request."`
		} `cmd:"" help:"Inspect host readiness and verify package and configuration without primary ownership."`
	} `cmd:"" help:"Run the host service for this state root."`
	State       stateCommands       `cmd:"" help:"Initialize, back up, and migrate task state with all writers stopped."`
	Launch      launchCommand       `cmd:"" help:"Launch an isolated worker and dispatch its assignment through the host."`
	Supervise   supervisionCommands `cmd:"" help:"Monitor workers and recover missing agents."`
	Control     controlCommands     `cmd:"" help:"Inspect and control live Herdr agents."`
	Primary     primaryCommands     `cmd:"" help:"Manage the root session owner through Linux /proc lifecycle hooks."`
	Root        string              `help:"Absolute Concierge state root; defaults to .concierge in the current directory." env:"CONCIERGE_HOME"`
	MessageRoot string              `help:"Absolute message root; defaults to .tincan in the current directory." env:"TINCAN_HOME"`
	Recipient   string              `help:"Primary inbox recipient." default:"${primaryRecipient}"`
	Backlog     backlogCommands     `cmd:"" help:"Queue and inspect authoritative task records."`
	Worker      struct {
		List struct{} `cmd:"" help:"List confirmed workers and pending adoptions."`
		Show struct {
			TaskID string `arg:"" help:"Task ID."`
		} `cmd:"" help:"Show a worker's launch identity and recovery markers."`
	} `cmd:"" help:"Inspect local worker bindings."`
	Task  taskCommands `cmd:"" help:"Inspect local tasks and exchange assignments."`
	Brief struct {
		Limit int `help:"Maximum rows from each source." default:"10"`
	} `cmd:"" default:"1" help:"Show root context from task records and pending messages."`
}

// Run executes Concierge without exiting the caller's process.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "--version", "-v", "-V":
			fmt.Fprintln(stdout, version)
			return 0
		}
	}
	var command cli
	exitCode := -1
	parser, err := kong.New(
		&command,
		kong.Name("concierge"),
		kong.Description("Local task intake and worker supervision."),
		kong.Writers(stdout, stderr),
		kong.Vars{"primaryRecipient": taskops.DefaultPrimaryRecipient},
		kong.Exit(func(code int) { exitCode = code }),
	)
	if err != nil {
		return fail(stdout, 2, err, cliHelp)
	}
	selected, err := parser.Parse(args)
	if exitCode >= 0 {
		return exitCode
	}
	if err != nil {
		return fail(stdout, 2, err, cliHelp)
	}
	command.hostExpectations.MessageRoot = command.MessageRoot
	if strings.HasPrefix(selected.Command(), "supervise ") {
		command.supervisionExpectations = &hostservice.SupervisionSettings{}
		if selected.Command() == "supervise pane" || selected.Command() == "supervise stop pane" {
			command.hostExpectations.Session = command.Supervise.Session
		}
		if os.Getenv("TINCAN_HERDR") != "" {
			executable, err := resolveHerdrExecutable(command.Supervise.Herdr)
			if err != nil {
				return fail(stdout, 2, err, supervisionHelp)
			}
			command.hostExpectations.HerdrExecutable = executable
		}
	}
	if selected.Command() == "control start" || selected.Command() == "control interrupt <target>" {
		command.hostExpectations.Session = os.Getenv("HERDR_SESSION")
	}
	if (selected.Command() == "control start" || selected.Command() == "control interrupt <target>") &&
		os.Getenv("TINCAN_HERDR") != "" {
		executable, err := resolveHerdrExecutable(command.Control.Herdr)
		if err != nil {
			return fail(stdout, 2, err, "provide a valid --herdr executable")
		}
		command.hostExpectations.HerdrExecutable = executable
	}
	for _, trace := range selected.Path {
		if trace.Flag == nil {
			continue
		}
		switch trace.Flag.Name {
		case "interval":
			if command.supervisionExpectations != nil {
				command.supervisionExpectations.Interval = &command.Supervise.Interval
			}
		case "restart-tolerance":
			if command.supervisionExpectations != nil {
				command.supervisionExpectations.RestartTolerance = &command.Supervise.RestartTolerance
			}
		case "restart-window":
			if command.supervisionExpectations != nil {
				command.supervisionExpectations.RestartWindow = &command.Supervise.RestartWindow
			}
		case "recipient":
			command.hostExpectations.Recipient = command.Recipient
		case "herdr":
			executable, err := resolveHerdrExecutable(fmt.Sprint(selected.FlagValue(trace.Flag)))
			if err != nil {
				return fail(stdout, 2, err, "provide a valid --herdr executable")
			}
			command.hostExpectations.HerdrExecutable = executable
		case "session":
			if selected.Command() == "control start" ||
				selected.Command() == "control interrupt <target>" ||
				selected.Command() == "supervise pane" || selected.Command() == "supervise stop pane" {
				command.hostExpectations.Session = fmt.Sprint(selected.FlagValue(trace.Flag))
			}
		}
	}
	switch selected.Command() {
	case "control status <target>":
		return command.Control.runStatus(ctx, stdout)
	}
	if selected.Command() == "backlog finish <task-id>" {
		if err := validateCompletion(command.Backlog.Finish.Mode, command.Backlog.Finish.ClosedOn); err != nil {
			return fail(
				stdout,
				2,
				err,
				"close requires --closed-on YYYY-MM-DD; retain forbids --closed-on",
			)
		}
	}
	if selected.Command() == "backlog resolve <reply-id>" {
		if err := validateCompletion(command.Backlog.Resolve.Mode, command.Backlog.Resolve.ClosedOn); err != nil {
			return fail(
				stdout,
				2,
				err,
				"close requires --closed-on YYYY-MM-DD; retain forbids --closed-on",
			)
		}
	}
	root, err := rootWithDefault(command.Root, ".concierge")
	if err != nil {
		return fail(stdout, 1, err, "provide an absolute --root path")
	}
	if _, err := firstmate.ParseAbsolutePath(root); err != nil {
		return fail(stdout, 2, err, "use an absolute, clean --root path")
	}
	switch selected.Command() {
	case "control interrupt <target>", "control start":
		return command.Control.runMutation(
			ctx,
			root,
			selected.Command(),
			command.hostExpectations,
			stdout,
		)
	case "launch <task-id>":
		return command.Launch.run(ctx, root, command.hostExpectations, stdin, stdout)
	case "host status":
		return command.runHostStatus(ctx, root, stdout)
	case "host serve":
		return command.runHost(ctx, root, stdout)
	case "state init":
		return runStateInitialization(ctx, root, stdout)
	case "state backup", "state migrate", "state recover":
		return command.State.run(ctx, root, selected.Command(), stdout)
	case "supervise task <task-id>",
		"supervise pane",
		"supervise stop task <task-id>",
		"supervise stop pane":
		return command.runSupervision(ctx, root, selected.Command(), stdout)
	case "primary claim", "primary verify", "primary release":
		return runPrimary(root, selected.Command(), stdin, stdout, primary.CurrentHookIdentity)
	case "task stop <task-id>", "task cancel <task-id>", "task abandon <task-id>":
		return command.Task.runControl(
			ctx,
			root,
			selected.Command(),
			command.hostExpectations,
			stdout,
		)
	case "task adopt <task-id>":
		return command.Task.Adopt.run(ctx, root, command.hostExpectations, stdout)
	case "task reply send <task-id>", "task reply check <message-id>", "task send <task-id>",
		"task receive <task-id> <message-id>",
		"task ack <task-id> <message-id>":
		return command.runTaskMessages(ctx, root, selected.Command(), stdin, stdout)
	case "backlog resolve <reply-id>":
		return command.runResolve(ctx, root, stdout)
	case "worker list", "worker show <task-id>", "task list", "task pending":
		return runRecordView(ctx, root, selected.Command(), command.Worker.Show.TaskID, stdout)
	case "brief":
		return command.runBrief(ctx, root, stdout)
	default:
		return command.Backlog.run(ctx, root, selected.Command(), stdin, stdout)
	}
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func fail(output io.Writer, code int, err error, help string) int {
	fmt.Fprintf(output, "error: %s\nhelp: %s\n", quote(err.Error()), quote(help))
	return code
}

func rootWithDefault(value, directoryName string) (string, error) {
	if value != "" {
		return value, nil
	}
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, directoryName), nil
}
