package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
)

const adoptUsage = "concierge task adopt TASK_ID --generation GEN --name NAME --session SESSION --pane PANE --project ABS --worktree ABS --agent-arg ARG [--agent-arg ARG...]"

type adoptCommand struct {
	TaskID     string   `arg:"" help:"Task ID."`
	Generation string   `required:"" help:"Generation supplied through the worker launch settings."`
	Name       string   `required:"" help:"Verified Herdr worker name."`
	Session    string   `required:"" help:"Herdr session containing the worker."`
	Pane       string   `required:"" help:"Exact worker pane."`
	Project    string   `required:"" help:"Absolute project directory."`
	Worktree   string   `required:"" help:"Absolute worker worktree directory."`
	AgentArgs  []string `name:"agent-arg" required:"" sep:"none" help:"Exact launch argument; repeat in launch order."`
}

func (command *adoptCommand) run(
	ctx context.Context,
	root string,
	expectations hostservice.Expectations,
	output io.Writer,
) int {
	response, err := hostservice.Call(ctx, filepath.Join(root, "host.sock"), hostservice.Request{
		Operation: hostservice.Adopt, Expectations: &expectations,
		Adoption: &taskops.AdoptionInput{
			TaskID:     command.TaskID,
			Generation: command.Generation,
			Name:       command.Name,
			Session:    command.Session,
			Pane:       command.Pane,
			Project:    command.Project,
			Worktree:   command.Worktree,
			AgentArgs:  command.AgentArgs,
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
			adoptUsage+"; inspect worker show TASK_ID and the live worker before retrying the identical binding",
		)
	}
	task, err := taskstate.NewMigrated(*response.Transition)
	if err != nil {
		return fail(output, 1, err, adoptUsage)
	}

	if _, err := fmt.Fprintf(output, "task: %s\nstate: %s\ngeneration: %s\n", quote(task.ID()), task.State(), quote(task.Snapshot().Worker.Generation)); err != nil {
		return 1
	}
	return 0
}
