package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

type cli struct {
	Root   string        `help:"Absolute Tincan state root." required:""`
	Adopt  adoptCommand  `cmd:"" help:"Record a live local Herdr worker as a task."`
	Stop   stopCommand   `cmd:"" help:"Close the recorded worker pane for a local task."`
	Cancel cancelCommand `cmd:"" help:"Release a pending adoption without task metadata."`
	List   listCommand   `cmd:"" help:"List local task metadata."`
}

type listCommand struct{}

type stopCommand struct {
	TaskID string `arg:"" help:"Local task ID whose worker pane should close."`
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
	return "usage: tincan-task --root ROOT adopt TASK_ID --session SESSION --pane PANE --name NAME --project PROJECT --worktree WORKTREE --agent-arg ARG; or stop TASK_ID; or cancel TASK_ID; or list"
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
	case "stop <task-id>":
		registry, err := crew.New(command.Root)
		if err != nil {
			return fail(output, 2, err, help())
		}
		if err := registry.WithCurrentTask(command.Stop.TaskID, func(task *crew.CurrentTask) error {
			return task.StopPane(ctx, client)
		}); err != nil {
			return fail(
				output,
				1,
				err,
				"retry tincan-task --root ROOT stop TASK_ID after resolving the reported state",
			)
		}
		fmt.Fprintf(
			output,
			"task: %s\nstate: pane_closed\nhelp: tincan-backlog --root ROOT finish %s --mode close|retain\n",
			command.Stop.TaskID,
			command.Stop.TaskID,
		)
	case "list":
		state, err := firstmate.ParseAbsolutePath(filepath.Join(command.Root, "tasks"))
		if err != nil {
			return fail(output, 2, err, "use an absolute, clean --root path")
		}
		tasks, err := firstmate.ListTasks(state)
		if err != nil {
			return fail(output, 1, err, "inspect the Tincan task state")
		}
		fmt.Fprintf(output, "tasks: %d found\n", len(tasks))
		if len(tasks) > 0 {
			fmt.Fprintf(output, "tasks[%d]{task,kind,worktree}:\n", len(tasks))
			for _, task := range tasks {
				fmt.Fprintf(output, "  %q,%q,%q\n", task.TaskID(), task.Kind(), task.Worktree())
			}
		}
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
