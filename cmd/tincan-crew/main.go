package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
)

type cli struct {
	Root     string          `help:"Absolute Tincan state root." required:""`
	Register registerCommand `cmd:"" help:"Record launch details for an existing local task."`
	Show     showCommand     `cmd:"" help:"Show one registered worker."`
	List     listCommand     `cmd:"" help:"List registered workers."`
}

type registerCommand struct {
	TaskID    string   `arg:"" help:"Existing local task ID."`
	Name      string   `help:"Herdr worker name, unique within its session." required:""`
	AgentArgs []string `name:"agent-arg" sep:"none" help:"Worker launch argument; repeat for resume identity."`
}

type showCommand struct {
	TaskID string `arg:"" help:"Registered local task ID."`
}

type listCommand struct{}

func quoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func failure(stdout io.Writer, code int, err error, help string) int {
	fmt.Fprintf(stdout, "error: %s\n", quoted(err.Error()))
	fmt.Fprintf(stdout, "help: %s\n", quoted(help))
	return code
}

func commandHelp(args []string) string {
	for _, argument := range args {
		switch argument {
		case "register":
			return "register flags: --root, --name, --agent-arg, --help; usage: tincan-crew --root ROOT register TASK_ID --name NAME --agent-arg ARG"
		case "show":
			return "show flags: --root, --help; usage: tincan-crew --root ROOT show TASK_ID"
		case "list":
			return "list flags: --root, --help; usage: tincan-crew --root ROOT list"
		}
	}
	return "commands: register, show, list; global flags: --root, --help"
}

func run(args []string, stdout io.Writer) int {
	var command cli
	parser, err := kong.New(
		&command,
		kong.Name("tincan-crew"),
		kong.Description("Keep local worker launch details for recovery."),
		kong.Writers(stdout, io.Discard),
	)
	if err != nil {
		return failure(stdout, 2, err, commandHelp(args))
	}
	context, err := parser.Parse(args)
	if err != nil {
		return failure(stdout, 2, err, commandHelp(args))
	}
	registry, err := crew.New(command.Root)
	if err != nil {
		return failure(stdout, 2, err, "use an absolute, clean --root path")
	}
	switch context.Command() {
	case "register <task-id>":
		return register(&command, registry, stdout)
	case "show <task-id>":
		if _, err := firstmate.ParseTaskID(command.Show.TaskID); err != nil {
			return failure(stdout, 2, err, commandHelp([]string{"show"}))
		}
		record, err := registry.Get(command.Show.TaskID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return failure(
					stdout,
					1,
					fmt.Errorf("no crew record for task %s", command.Show.TaskID),
					fmt.Sprintf(
						"tincan-crew --root %s register %s --name NAME --agent-arg ARG",
						command.Root,
						command.Show.TaskID,
					),
				)
			}
			return failure(stdout, 1, err, "inspect the crew state directory")
		}
		printRecord(stdout, record)
	case "list":
		records, err := registry.List()
		if err != nil {
			return failure(stdout, 1, err, "inspect the crew state directory")
		}
		fmt.Fprintf(stdout, "count: %d\n", len(records))
		fmt.Fprintf(stdout, "crew[%d]{task,name,session,pane_id}:\n", len(records))
		for _, record := range records {
			fmt.Fprintf(
				stdout,
				"  %s,%s,%s,%s\n",
				record.TaskID,
				record.Name,
				record.HerdrSession,
				record.PaneID,
			)
		}
	default:
		return failure(
			stdout,
			2,
			fmt.Errorf("unknown command %q", context.Command()),
			commandHelp(args),
		)
	}
	return 0
}

func register(command *cli, registry *crew.Registry, stdout io.Writer) int {
	if err := crew.ValidateLaunch(command.Register.Name, command.Register.AgentArgs); err != nil {
		return failure(
			stdout,
			2,
			fmt.Errorf("provide a valid --name and at least one valid --agent-arg"),
			commandHelp([]string{"register"}),
		)
	}
	state, err := firstmate.ParseAbsolutePath(filepath.Join(command.Root, "tasks"))
	if err != nil {
		return failure(stdout, 2, err, "use an absolute, clean --root path")
	}
	id, err := firstmate.ParseTaskID(command.Register.TaskID)
	if err != nil {
		return failure(stdout, 2, err, commandHelp([]string{"register"}))
	}
	lock, err := firstmate.AcquireTaskLock(state, id, 2*time.Second)
	if err != nil {
		if errors.Is(err, firstmate.ErrLockTimeout) {
			return failure(stdout, 1, err, "retry after the task metadata lock is released")
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, firstmate.ErrInvalidPath) {
			return failure(stdout, 1, fmt.Errorf("local task state is missing or invalid"),
				"create the local task state directory before registering its worker")
		}
		return failure(stdout, 1, err, "inspect the local task state directory")
	}
	record, operationErr := registerLocked(command, registry, state, id)
	if releaseErr := lock.Release(); releaseErr != nil {
		return failure(
			stdout,
			1,
			errors.Join(operationErr, releaseErr),
			"inspect the task metadata lock before retrying registration",
		)
	}
	if operationErr != nil {
		if errors.Is(operationErr, os.ErrNotExist) {
			return failure(stdout, 1, fmt.Errorf("local task %s has no metadata", id),
				"create local task metadata before registering its worker")
		}
		if errors.Is(operationErr, crew.ErrInvalidRecord) {
			return failure(stdout, 2, operationErr, commandHelp([]string{"register"}))
		}
		if errors.Is(operationErr, crew.ErrLockTimeout) {
			return failure(
				stdout,
				1,
				operationErr,
				"retry registration after the other registry writer exits",
			)
		}
		return failure(
			stdout,
			1,
			operationErr,
			fmt.Sprintf("tincan-crew --root %s list", command.Root),
		)
	}
	printRecord(stdout, record)
	return 0
}

func registerLocked(
	command *cli,
	registry *crew.Registry,
	state firstmate.AbsolutePath,
	id firstmate.TaskID,
) (crew.Record, error) {
	metadata, err := firstmate.ReadTask(state, id)
	if err != nil {
		return crew.Record{}, err
	}
	record := crew.Record{
		TaskID: id.String(), Name: command.Register.Name, Kind: metadata.Harness(),
		PaneID: metadata.HerdrPaneID(), HerdrSession: metadata.HerdrSession(),
		Project:  metadata.Project().String(),
		Worktree: metadata.Worktree().String(), SpawnGeneration: metadata.SpawnGeneration(),
		AgentArgs: command.Register.AgentArgs,
	}
	if err := registry.Register(record); err != nil {
		return crew.Record{}, err
	}
	return record, nil
}

func printRecord(stdout io.Writer, record crew.Record) {
	fmt.Fprintf(stdout, "task: %s\n", record.TaskID)
	fmt.Fprintf(stdout, "name: %s\n", record.Name)
	fmt.Fprintf(stdout, "kind: %s\n", record.Kind)
	fmt.Fprintf(stdout, "pane_id: %s\n", record.PaneID)
	fmt.Fprintf(stdout, "herdr_session: %s\n", record.HerdrSession)
	fmt.Fprintf(stdout, "project: %s\n", quoted(record.Project))
	fmt.Fprintf(stdout, "worktree: %s\n", quoted(record.Worktree))
	fmt.Fprintf(stdout, "spawn_generation: %s\n", record.SpawnGeneration)
	if record.PendingGeneration != "" {
		fmt.Fprintf(stdout, "pending_generation: %s\n", record.PendingGeneration)
	}
	fmt.Fprintf(stdout, "agent_args[%d]:\n", len(record.AgentArgs))
	for _, argument := range record.AgentArgs {
		fmt.Fprintf(stdout, "  %s\n", quoted(argument))
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
