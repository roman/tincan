package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
)

type cli struct {
	Root string     `help:"Absolute Tincan state root." required:""`
	Add  addCommand `cmd:"" help:"Queue a task in the root backlog."`
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
	return "usage: tincan-backlog --root ROOT add ID TITLE --kind KIND --repo REPO [--body-file PATH] [--blocked-by ID]"
}

func fail(output io.Writer, code int, err error, next string) int {
	fmt.Fprintf(output, "error: %q\nhelp: %s\n", err, next)
	return code
}

func run(arguments []string, output io.Writer) int {
	var command cli
	parser, err := kong.New(&command, kong.Name("tincan-backlog"), kong.Writers(output, io.Discard))
	if err != nil {
		return fail(output, 2, err, help())
	}
	selected, err := parser.Parse(arguments)
	if err != nil {
		return fail(output, 2, err, help())
	}
	if selected.Command() != "add <task-id> <title>" {
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
			fmt.Errorf("Tincan root must be an existing directory: %s", root.String()),
			"create the local Tincan state root first",
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
