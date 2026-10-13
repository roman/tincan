package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskops"
)

type launchCommand struct {
	TaskID     string   `arg:"" help:"Queued task ID."`
	Harness    string   `required:"" enum:"codex,claude" help:"Worker harness."`
	Project    string   `required:"" help:"Absolute source repository path."`
	Worktree   string   `required:"" help:"Absolute isolated worktree path."`
	Branch     string   `required:"" help:"New literal worktree branch."`
	BaseCommit string   `required:"" help:"Full commit ID from the source repository."`
	Workspace  string   `required:"" help:"Herdr workspace for the worker tab."`
	AgentArgs  []string `name:"agent-arg" sep:"none" help:"Worker launch argument; repeat in order, using --agent-arg=VALUE for options."`
	BodyFile   string   `required:"" help:"Assignment file, or - for stdin."`
}

func (command *launchCommand) run(
	ctx context.Context,
	root string,
	expectations hostservice.Expectations,
	stdin io.Reader,
	output io.Writer,
) int {
	body, err := readBodyFile(command.BodyFile, stdin)
	if err != nil {
		return fail(output, 1, err, "provide a readable --body-file or use --body-file - for stdin")
	}
	if err := taskops.ValidateInitialLaunch(command.Harness, command.AgentArgs, body); err != nil {
		return fail(output, 2, err, "provide a nonblank assignment and compatible worker arguments")
	}
	response, err := hostservice.Call(ctx, filepath.Join(root, "host.sock"), hostservice.Request{
		Operation: hostservice.Launch, Expectations: &expectations,
		Launch: &hostservice.LaunchRequest{
			TaskID: command.TaskID, Harness: command.Harness, Project: command.Project,
			Worktree: command.Worktree, Branch: command.Branch, BaseCommit: command.BaseCommit,
			Workspace: command.Workspace, AgentArgs: command.AgentArgs, Body: body,
		},
	})
	if err == nil && response.Outcome != hostservice.Completed {
		err = errors.New(response.Error)
	}
	if err != nil {
		code := 1
		if response.Outcome == hostservice.Invalid {
			code = 2
		}
		return fail(
			output,
			code,
			err,
			"inspect concierge backlog show TASK_ID and worker show TASK_ID before another launch; an unknown outcome can include an active worker",
		)
	}
	worker := response.Transition.Worker
	_, err = fmt.Fprintf(
		output,
		"task: %s\nstate: %s\ngeneration: %s\nsession: %s\npane: %s\n",
		quote(command.TaskID),
		response.Transition.State,
		quote(worker.Generation),
		quote(worker.Session),
		quote(worker.Pane),
	)
	if err != nil {
		return 1
	}
	return 0
}
