package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/herdr"
)

type cli struct {
	Root   string        `help:"Absolute Tincan state root." required:""`
	Adopt  adoptCommand  `cmd:"" help:"Record a live local Herdr worker as a task."`
	Cancel cancelCommand `cmd:"" help:"Release a pending adoption without task metadata."`
}

type cancelCommand struct {
	TaskID string `arg:"" help:"Pending local task ID."`
}

type adoptCommand struct {
	TaskID    string   `arg:"" help:"Local task ID."`
	Session   string   `help:"Herdr session containing the worker." required:""`
	Pane      string   `help:"Herdr pane containing the worker." required:""`
	Name      string   `help:"Herdr worker name for restart." required:""`
	Project   string   `help:"Absolute project directory." required:""`
	Worktree  string   `help:"Absolute worker directory." required:""`
	Kind      string   `help:"Task role." default:"crewmate"`
	AgentArgs []string `name:"agent-arg" sep:"none" help:"Worker restart argument; repeat for each argument."`
}

func help() string {
	return "usage: tincan-task --root ROOT adopt TASK_ID --session SESSION --pane PANE --name NAME --project PROJECT --worktree WORKTREE --agent-arg ARG; or cancel TASK_ID"
}

func fail(output io.Writer, code int, err error, next string) int {
	fmt.Fprintf(output, "error: %q\nhelp: %s\n", err, next)
	return code
}

func run(ctx context.Context, arguments []string, output io.Writer, client herdr.Client) int {
	var command cli
	parser, err := kong.New(&command, kong.Name("tincan-task"), kong.Writers(output, io.Discard))
	if err != nil {
		return fail(output, 2, err, help())
	}
	selected, err := parser.Parse(arguments)
	if err != nil {
		return fail(output, 2, err, help())
	}
	switch selected.Command() {
	case "adopt <task-id>":
		if err := adopt(ctx, command, client); err != nil {
			return fail(
				output,
				1,
				err,
				"retry if task metadata exists; cancel a pending reservation without metadata when the worker is gone",
			)
		}
		fmt.Fprintf(
			output,
			"task: %s\nworker: %s\nstate: adopted\n",
			command.Adopt.TaskID,
			command.Adopt.Name,
		)
	case "cancel <task-id>":
		registry, err := crew.New(command.Root)
		if err != nil {
			return fail(output, 2, err, help())
		}
		if err := registry.CancelPendingAdoption(command.Cancel.TaskID); err != nil {
			return fail(
				output,
				1,
				err,
				"cancel requires a pending reservation and no task metadata",
			)
		}
		fmt.Fprintf(output, "task: %s\nstate: cancelled\n", command.Cancel.TaskID)
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected.Command()), help())
	}
	return 0
}

func adopt(ctx context.Context, command cli, client herdr.Client) error {
	registry, err := crew.New(command.Root)
	if err != nil {
		return err
	}
	input := command.Adopt
	return registry.AdoptLocalTask(ctx, crew.Adoption{
		TaskID: input.TaskID, Session: input.Session, Pane: input.Pane,
		Name: input.Name, Project: input.Project, Worktree: input.Worktree,
		Kind: input.Kind, AgentArgs: input.AgentArgs,
	}, client)
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, herdr.NewClient("")))
}
