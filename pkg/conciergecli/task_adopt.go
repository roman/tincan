package conciergecli

import (
	"context"
	"fmt"
	"io"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/taskops"
)

const adoptUsage = "concierge task adopt TASK_ID --name NAME --session SESSION --pane PANE --project ABS --worktree ABS --agent-arg ARG [--agent-arg ARG...]"

type adoptCommand struct {
	TaskID    string   `arg:"" help:"Task ID."`
	Name      string   `required:"" help:"Verified Herdr worker name."`
	Session   string   `required:"" help:"Herdr session containing the worker."`
	Pane      string   `required:"" help:"Exact worker pane."`
	Project   string   `required:"" help:"Absolute project directory."`
	Worktree  string   `required:"" help:"Absolute worker worktree directory."`
	AgentArgs []string `name:"agent-arg" required:"" sep:"none" help:"Exact launch argument; repeat in launch order."`
}

func (command *adoptCommand) run(ctx context.Context, root string, output io.Writer) int {
	service, err := taskops.New(root)
	if err != nil {
		return fail(output, 2, err, adoptUsage)
	}
	task, err := service.AdoptWorker(ctx, taskops.AdoptionInput{
		TaskID: command.TaskID, Name: command.Name, Session: command.Session, Pane: command.Pane,
		Project: command.Project, Worktree: command.Worktree, AgentArgs: command.AgentArgs,
	}, herdr.NewClient("herdr"))
	if err != nil {
		return fail(
			output,
			1,
			err,
			adoptUsage+"; inspect worker show TASK_ID and the live worker before retrying the identical binding",
		)
	}
	if _, err := fmt.Fprintf(output, "task: %s\nstate: %s\ngeneration: %s\n", quote(task.ID()), task.State(), quote(task.Snapshot().Worker.Generation)); err != nil {
		return 1
	}
	return 0
}
