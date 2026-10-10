package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

type cli struct {
	Root   string        `help:"Absolute Concierge state root." required:""`
	Add    addCommand    `cmd:"" help:"Queue a task in the root backlog."`
	List   listCommand   `cmd:"" help:"List backlog tasks without the startup row limit."`
	Start  startCommand  `cmd:"" help:"Start a queued task with a current worker binding."`
	Finish finishCommand `cmd:"" help:"Complete a local task after its Herdr pane is gone."`
}

type listCommand struct {
	State string `help:"Backlog state to list." enum:"queued,in_flight,held,done" default:"queued"`
}

type startCommand struct {
	TaskID string `arg:"" help:"Queued task ID."`
}

type finishCommand struct {
	TaskID string `arg:"" help:"In-flight task ID."`
	Mode   string `help:"Close the row or retain a held row in the queue." enum:"close,retain" default:"close"`
}

type addCommand struct {
	TaskID   string   `arg:"" help:"Stable task ID."`
	Title    string   `arg:"" help:"Task title."`
	Kind     string   `help:"Task kind." required:""`
	Repo     string   `help:"Repository name." required:""`
	BodyFile string   `help:"Path to a Markdown task body."`
	Blocked  []string `name:"blocked-by" help:"Task dependency; repeat for each dependency."`
}

type addResult struct {
	OK      bool `json:"ok"`
	Already bool `json:"already"`
	Task    struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		State string `json:"state"`
		Kind  string `json:"kind"`
		Repo  string `json:"repo"`
	} `json:"task"`
}

func help() string {
	return "usage: concierge-backlog --root ROOT add ID TITLE --kind KIND --repo REPO; or list [--state STATE]; or start ID; or finish ID --mode close|retain"
}

func fail(output io.Writer, code int, err error, next string) int {
	fmt.Fprintf(output, "error: %q\nhelp: %s\n", err, next)
	return code
}

func run(arguments []string, output io.Writer) int {
	return runWithClient(context.Background(), arguments, output, herdr.NewClient(""))
}

func runWithClient(
	ctx context.Context,
	arguments []string,
	output io.Writer,
	client herdr.Client,
) int {
	var command cli
	parser, err := kong.New(
		&command,
		kong.Name("concierge-backlog"),
		kong.Writers(output, io.Discard),
	)
	if err != nil {
		return fail(output, 2, err, help())
	}
	selected, err := parser.Parse(arguments)
	if err != nil {
		return fail(output, 2, err, help())
	}
	if selected.Command() != "add <task-id> <title>" && selected.Command() != "list" &&
		selected.Command() != "start <task-id>" && selected.Command() != "finish <task-id>" {
		return fail(output, 2, fmt.Errorf("unknown command %q", selected.Command()), help())
	}
	root, err := firstmate.ParseAbsolutePath(command.Root)
	if err != nil {
		return fail(output, 2, err, "use an absolute, clean --root path")
	}
	rootInfo, err := os.Lstat(root.String())
	if err != nil || !rootInfo.IsDir() {
		return fail(
			output,
			1,
			fmt.Errorf("Concierge state root must be an existing directory: %s", root.String()),
			"create the local Concierge state root first",
		)
	}
	backlogFile := filepath.Join(root.String(), "backlog.md")
	if info, err := os.Lstat(backlogFile); err == nil && !info.Mode().IsRegular() {
		return fail(
			output,
			1,
			fmt.Errorf("backlog is not a regular file: %s", backlogFile),
			"inspect the backlog path",
		)
	} else if err != nil && !os.IsNotExist(err) {
		return fail(output, 1, err, "inspect the backlog path")
	}
	if selected.Command() == "list" {
		if err := listBacklog(output, backlogFile, command.List.State); err != nil {
			return fail(output, 1, err, "inspect the backlog file and retry the list")
		}
		return 0
	}
	if selected.Command() == "start <task-id>" {
		result, err := startTask(root.String(), command.Start.TaskID, backlogFile)
		if err != nil {
			return fail(
				output,
				1,
				err,
				"inspect the task row and current worker binding; retry after correcting them",
			)
		}
		fmt.Fprintf(
			output,
			"task: %q\nstate: in_flight\nalready: %t\n",
			command.Start.TaskID,
			result.Already,
		)
		return 0
	}
	if selected.Command() == "finish <task-id>" {
		result, err := finishTask(
			ctx, root.String(), command.Finish.TaskID, backlogFile,
			command.Finish.Mode, client,
		)
		if err != nil {
			return fail(
				output,
				1,
				err,
				"inspect the backlog row, completion marker, and recorded Herdr pane; retry after correcting them",
			)
		}
		fmt.Fprintf(
			output, "task: %q\nstate: %s\nalready: %t\n",
			command.Finish.TaskID, result.State, result.Already,
		)
		return 0
	}
	args := []string{
		"add",
		command.Add.TaskID,
		command.Add.Title,
		"--kind",
		command.Add.Kind,
		"--repo",
		command.Add.Repo,
		"--queue",
		"--json",
	}
	if command.Add.BodyFile != "" {
		args = append(args, "--body-file", command.Add.BodyFile)
	}
	for _, dependency := range command.Add.Blocked {
		args = append(args, "--blocked-by", dependency)
	}
	args = append(args, "--file", backlogFile)
	process := exec.Command("tasks-axi", args...)
	var result bytes.Buffer
	process.Stdout = &result
	process.Stderr = &result
	if err := process.Run(); err != nil {
		return fail(
			output,
			1,
			fmt.Errorf("tasks-axi add failed: %w: %s", err, result.String()),
			"install tasks-axi 0.2.6 or newer and inspect the task input",
		)
	}
	var added addResult
	if err := json.Unmarshal(result.Bytes(), &added); err != nil || !added.OK ||
		added.Task.ID != command.Add.TaskID {
		return fail(
			output,
			1,
			fmt.Errorf("invalid tasks-axi add response: %s", result.String()),
			"inspect the backlog and tasks-axi version",
		)
	}
	if added.Already {
		return fail(
			output,
			1,
			fmt.Errorf("task %s already exists", added.Task.ID),
			"use a new task ID or inspect the existing backlog row",
		)
	}
	fmt.Fprintf(
		output,
		"task: %q\ntitle: %q\nstate: %s\nkind: %q\nrepo: %q\nbacklog: %q\n",
		added.Task.ID,
		added.Task.Title,
		added.Task.State,
		added.Task.Kind,
		added.Task.Repo,
		backlogFile,
	)
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
