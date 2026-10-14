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
	Workspace  string   `required:"" help:"Existing Herdr workspace ID for the worker tab (labels are unsupported)."`
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
	if err == nil &&
		(response.Outcome == hostservice.Pending || response.Outcome == hostservice.Blocked) {
		return writeLaunchProgress(output, command.TaskID, response)
	}
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

func writeLaunchProgress(output io.Writer, taskID string, response hostservice.Response) int {
	snapshot := response.Transition
	phase := "awaiting_supervision"
	tracking := "automatic"
	if snapshot.LaunchIntent == nil && response.Outcome == hostservice.Blocked {
		phase = "supervision_stopped"
		tracking = "stopped"
	}
	generation := ""
	if snapshot.Worker != nil {
		generation = snapshot.Worker.Generation
	}
	if intent := snapshot.LaunchIntent; intent != nil {
		phase = string(intent.Phase)
		generation = intent.Generation
	}
	if _, err := fmt.Fprintf(output, "task: %s\nlaunch: %s\ntracking: %s\ngeneration: %s\nphase: %s\nreason: %s\nhelp: %s\n",
		quote(taskID), response.Outcome, tracking, quote(generation), phase, quote(response.Error), quote("inspect completion with concierge worker show "+quote(taskID))); err != nil {
		return 1
	}
	if response.Checkpoint != nil {
		intent := snapshot.LaunchIntent
		if _, err := fmt.Fprintf(output, "checkpoint: %s\nnotification: %s\nsession: %s\npane: %s\nworktree: %s\ncheckpoint_help: %s\n", response.Checkpoint.Kind, response.Checkpoint.Notification, quote(intent.Session), quote(intent.Endpoint.Pane), quote(intent.Worktree), quote(hostservice.NativeCheckpointHelp(intent))); err != nil {
			return 1
		}
	}
	if response.Outcome == hostservice.Blocked {
		if snapshot.LaunchIntent == nil {
			if _, err := fmt.Fprintf(output, "recovery: %s\n", quote("inspect the worker, then request supervision with concierge supervise task "+quote(taskID))); err != nil {
				return 1
			}
		}
		if response.Checkpoint == nil && snapshot.LaunchIntent != nil && snapshot.Worker == nil {
			if _, err := fmt.Fprintf(output, "recovery: %s\n", quote("inspect the native checkpoint and launch resources; safe abandonment requires concierge task abandon "+quote(taskID)+" --generation "+quote(generation))); err != nil {
				return 1
			}
		}
		return 1
	}
	return 0
}
