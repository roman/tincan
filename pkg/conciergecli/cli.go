// Package conciergecli implements record-backed local task supervision commands.
package conciergecli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/taskops"
)

const version = "0.1.0"

const cliHelp = "commands: brief [--limit N], backlog, worker list|show, task list|pending|send|receive|ack; global flags: --root, --message-root, --recipient, --help; run concierge COMMAND --help for arguments"

type cli struct {
	Root        string          `help:"Absolute Concierge state root; defaults to .concierge in the current directory." env:"CONCIERGE_HOME"`
	MessageRoot string          `help:"Absolute message root; defaults to .tincan in the current directory." env:"TINCAN_HOME"`
	Recipient   string          `help:"Primary inbox recipient." default:"${primaryRecipient}"`
	Backlog     backlogCommands `cmd:"" help:"Queue and inspect authoritative task records."`
	Worker      struct {
		List struct{} `cmd:"" help:"List confirmed workers and pending adoptions."`
		Show struct {
			TaskID string `arg:"" help:"Task ID."`
		} `cmd:"" help:"Show a worker's launch identity and recovery markers."`
	} `cmd:"" help:"Inspect local worker bindings."`
	Task  taskCommands `cmd:"" help:"Inspect local tasks and exchange assignments."`
	Brief struct {
		Limit int `help:"Maximum rows from each source." default:"10"`
	} `cmd:"" default:"1" help:"Show root context from task records and pending messages."`
}

// Run executes Concierge without exiting the caller's process.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 {
		switch args[0] {
		case "--version", "-v", "-V":
			fmt.Fprintln(stdout, version)
			return 0
		}
	}
	var command cli
	exitCode := -1
	parser, err := kong.New(
		&command,
		kong.Name("concierge"),
		kong.Description("Local task intake and worker supervision."),
		kong.Writers(stdout, stderr),
		kong.Vars{"primaryRecipient": taskops.DefaultPrimaryRecipient},
		kong.Exit(func(code int) { exitCode = code }),
	)
	if err != nil {
		return fail(stdout, 2, err, cliHelp)
	}
	selected, err := parser.Parse(args)
	if exitCode >= 0 {
		return exitCode
	}
	if err != nil {
		return fail(stdout, 2, err, cliHelp)
	}
	if selected.Command() == "backlog finish <task-id>" {
		if err := validateCompletion(command.Backlog.Finish.Mode, command.Backlog.Finish.ClosedOn); err != nil {
			return fail(
				stdout,
				2,
				err,
				"close requires --closed-on YYYY-MM-DD; retain forbids --closed-on",
			)
		}
	}
	if selected.Command() == "backlog resolve <reply-id>" {
		if err := validateCompletion(command.Backlog.Resolve.Mode, command.Backlog.Resolve.ClosedOn); err != nil {
			return fail(
				stdout,
				2,
				err,
				"close requires --closed-on YYYY-MM-DD; retain forbids --closed-on",
			)
		}
	}
	root, err := rootWithDefault(command.Root, ".concierge")
	if err != nil {
		return fail(stdout, 1, err, "provide an absolute --root path")
	}
	if _, err := firstmate.ParseAbsolutePath(root); err != nil {
		return fail(stdout, 2, err, "use an absolute, clean --root path")
	}
	switch selected.Command() {
	case "task send <task-id>",
		"task receive <task-id> <message-id>",
		"task ack <task-id> <message-id>":
		return command.runTaskMessages(ctx, root, selected.Command(), stdin, stdout)
	case "backlog resolve <reply-id>":
		return command.runResolve(ctx, root, stdout)
	case "worker list", "worker show <task-id>", "task list", "task pending":
		return runRecordView(ctx, root, selected.Command(), command.Worker.Show.TaskID, stdout)
	case "brief":
		return command.runBrief(ctx, root, stdout)
	default:
		return command.Backlog.run(ctx, root, selected.Command(), stdin, stdout)
	}
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func fail(output io.Writer, code int, err error, help string) int {
	fmt.Fprintf(output, "error: %s\nhelp: %s\n", quote(err.Error()), quote(help))
	return code
}

func rootWithDefault(value, directoryName string) (string, error) {
	if value != "" {
		return value, nil
	}
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, directoryName), nil
}
