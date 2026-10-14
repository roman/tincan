package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
)

const resolveUsage = "concierge backlog resolve REPLY_ID [--mode close|retain] [--closed-on YYYY-MM-DD]"

const backlogHelp = "global flags: --root, --help; commands: backlog add ID TITLE --kind KIND --repo REPO [--body-file FILE|-] [--blocked-by ID]; backlog list [--state queued|in_flight|held|done]; backlog show ID [--full]; backlog start|hold|release ID; backlog finish|cleanup ID --generation GEN; backlog release-worktree ID --generation GEN; run concierge backlog COMMAND --help for arguments; " + resolveUsage

type backlogCommands struct {
	ReleaseWorktree struct {
		TaskID     string `arg:"" help:"Task ID."`
		Generation string `required:"" help:"Retired or abandoned generation whose lease to return."`
	} `cmd:"" help:"Return a retired or abandoned worktree lease while preserving its branch."`
	Resolve struct {
		ReplyID  string `arg:"" help:"Bound reply message ID in the primary inbox."`
		Mode     string `enum:"close,retain" default:"close" help:"Close the task or retain held work in the queue."`
		ClosedOn string `help:"Closing date YYYY-MM-DD; required for close, forbidden for retain."`
	} `cmd:"" help:"Accept a bound result and acknowledge its primary reply."`

	Start struct {
		TaskID string `arg:"" help:"Task ID."`
	} `cmd:"" help:"Start a confirmed worker after checking holds and dependencies."`
	Finish struct {
		TaskID     string `arg:"" help:"Task ID."`
		Generation string `required:"" help:"Worker generation that produced the result."`
		Mode       string `enum:"close,retain" default:"close" help:"Close the task or retain held work in the queue."`
		ClosedOn   string `help:"Closing date YYYY-MM-DD; required for close, forbidden for retain."`
	} `cmd:"" help:"Accept a worker result without closing its pane."`
	Cleanup struct {
		TaskID     string `arg:"" help:"Task ID."`
		Generation string `required:"" help:"Accepted worker generation to retire."`
	} `cmd:"" help:"Retire an accepted result after its exact pane departs."`
	Hold struct {
		TaskID string `arg:"" help:"Task ID."`
		Reason string `required:"" help:"Reason for the hold."`
		Kind   string `help:"Optional hold kind: captain, external, load, parked, or future."`
		Until  string `help:"Optional date YYYY-MM-DD; records a date without scheduling release."`
	} `cmd:"" help:"Replace the task hold to block dispatch or close."`
	Release struct {
		TaskID string `arg:"" help:"Task ID."`
	} `cmd:"" help:"Remove a hold after any accepted completion is retired."`

	Add struct {
		TaskID   string   `arg:"" help:"Stable task ID."`
		Title    string   `arg:"" help:"Task title."`
		Kind     string   `help:"Task kind." required:""`
		Repo     string   `help:"Repository name." required:""`
		BodyFile string   `help:"Task body file; use - for stdin."`
		Blocked  []string `name:"blocked-by" sep:"none" help:"Blocking task ID; repeat for each dependency."`
	} `cmd:"" help:"Queue a task or confirm an identical intake retry."`
	List struct {
		State string `help:"Backlog state to list." enum:"queued,in_flight,held,done" default:"queued"`
	} `cmd:"" help:"List all tasks in the selected state."`
	Show struct {
		TaskID string `arg:"" help:"Task ID."`
		Full   bool   `help:"Show the complete title and body."`
	} `cmd:"" help:"Show one task record."`
}

func (command *backlogCommands) run(
	ctx context.Context,
	root, selected string,
	stdin io.Reader,
	output io.Writer,
) int {
	switch selected {
	case "backlog add <task-id> <title>":
		body := ""
		if command.Add.BodyFile != "" {
			var data []byte
			var err error
			if command.Add.BodyFile == "-" {
				data, err = io.ReadAll(stdin)
			} else {
				data, err = os.ReadFile(command.Add.BodyFile)
			}
			if err != nil {
				return fail(
					output,
					1,
					err,
					"provide a readable --body-file or use --body-file - for stdin",
				)
			}
			body = string(data)
		}
		definition := taskstate.Definition{
			ID:    command.Add.TaskID,
			Title: command.Add.Title,
			Kind:  command.Add.Kind,
			Repo:  command.Add.Repo,
			Body:  body,
		}
		for _, id := range command.Add.Blocked {
			definition.Dependencies = append(
				definition.Dependencies,
				taskstate.Dependency{Type: "blocked-by", ID: id},
			)
		}
		response, err := hostservice.Call(
			ctx,
			filepath.Join(root, "host.sock"),
			hostservice.Request{Operation: hostservice.Intake, Intake: &definition},
		)
		if err != nil {
			return fail(
				output,
				1,
				err,
				"run concierge --root ROOT host serve outside the model sandbox; backlog add also needs approved unsandboxed execution or verified socket permission; inspect the task before retrying identical intake",
			)
		}
		if response.Outcome != hostservice.Completed {
			code := 1
			if response.Outcome == hostservice.Invalid {
				code = 2
			}
			return fail(
				output,
				code,
				errors.New(response.Error),
				"use the current primary session; inspect backlog show TASK_ID and retry identical intake after correcting the reported error",
			)
		}
		task := response.Task
		_, err = fmt.Fprintf(
			output,
			"task: %s\ntitle: %s\nstate: %s\nkind: %s\nrepo: %s\nbacklog: %s\n",
			quote(task.Definition.ID),
			quote(definition.Title),
			task.State,
			quote(definition.Kind),
			quote(definition.Repo),
			quote(filepath.Join(root, "backlog.md")),
		)
		if err != nil {
			return 1
		}
		return 0
	case "backlog list", "backlog show <task-id>":
		store, err := taskstate.NewStore(root)
		if err != nil {
			return fail(output, 2, err, backlogHelp)
		}
		if selected == "backlog list" {
			tasks, err := store.List(ctx)
			if err != nil {
				return fail(
					output,
					1,
					err,
					"provide an initialized version 2 --root and inspect its task records",
				)
			}
			if err := taskview.WriteList(output, tasks, command.List.State); err != nil {
				return 1
			}
			return 0
		}
		task, err := store.Read(ctx, command.Show.TaskID)
		if err != nil {
			return fail(output, 1, err, "inspect the task ID and initialized version 2 --root")
		}
		detail := taskview.DetailSummary
		if command.Show.Full {
			detail = taskview.DetailFull
		}
		if err := taskview.WriteTask(output, task, detail); err != nil {
			return 1
		}
		return 0
	default:
		return command.runLifecycle(ctx, root, selected, output)
	}
}
