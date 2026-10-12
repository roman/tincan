package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/taskstate"
)

const version = "0.1.0"

var errHelpShown = errors.New("help shown")

type cli struct {
	Root    string         `help:"Absolute Concierge state root." required:""`
	Backup  string         `help:"Absolute sibling backup path." required:""`
	Copy    copyCommand    `cmd:"" name:"backup" help:"Copy the guarded version 1 state."`
	Migrate migrateCommand `cmd:"" help:"Back up and switch the task state to version 2."`
	Recover recoverCommand `cmd:"" help:"Verify and finish a pending version 2 switch."`
}

type copyCommand struct{}
type migrateCommand struct{}
type recoverCommand struct{}

func usage() string {
	return "usage: concierge-migrate --root ROOT --backup BACKUP backup|migrate|recover"
}

func fail(output io.Writer, code int, err error, next string) int {
	fmt.Fprintf(output, "error: %q\nhelp: %q\n", err, next)
	return code
}

func printHome(output io.Writer) {
	executable, err := os.Executable()
	if err == nil {
		if home, err := os.UserHomeDir(); err == nil &&
			strings.HasPrefix(executable, home+string(os.PathSeparator)) {
			executable = "~" + strings.TrimPrefix(executable, home)
		}
		fmt.Fprintf(output, "bin: %q\n", executable)
	}
	fmt.Fprintln(output, "description: Back up and migrate Concierge task state")
	fmt.Fprintln(output, usage())
	fmt.Fprintln(output, "commands: backup, migrate, recover")
}

func printExamples(output io.Writer, command string) {
	if command == "" {
		return
	}
	fmt.Fprintln(output, "Examples:")
	fmt.Fprintf(
		output,
		"  concierge-migrate --root /path/state --backup /path/state-backup %s\n",
		command,
	)
}

func run(ctx context.Context, arguments []string, output io.Writer) int {
	if len(arguments) == 0 {
		printHome(output)
		return 0
	}
	if len(arguments) == 1 {
		switch arguments[0] {
		case "-v", "-V", "--version":
			fmt.Fprintln(output, version)
			return 0
		}
	}
	var command cli
	parser, err := kong.New(
		&command,
		kong.Name("concierge-migrate"),
		kong.Writers(output, io.Discard),
		kong.Help(func(options kong.HelpOptions, selected *kong.Context) error {
			if err := kong.DefaultHelpPrinter(options, selected); err != nil {
				return err
			}
			printExamples(output, selected.Command())
			return errHelpShown
		}),
	)
	if err != nil {
		return fail(output, 2, err, usage())
	}
	selected, err := parser.Parse(arguments)
	if errors.Is(err, errHelpShown) {
		return 0
	}
	if err != nil {
		return fail(output, 2, err, usage())
	}
	if _, err := firstmate.ParseAbsolutePath(command.Root); err != nil {
		return fail(output, 2, err, "use an absolute, clean --root path")
	}
	if _, err := firstmate.ParseAbsolutePath(command.Backup); err != nil {
		return fail(output, 2, err, "use an absolute, clean --backup path")
	}
	switch selected.Command() {
	case "backup":
		if _, err := taskstate.BackupLegacy(ctx, command.Root, command.Backup); err != nil {
			return fail(
				output,
				1,
				err,
				"inspect the version 1 state and retry with a new backup path",
			)
		}
		fmt.Fprintf(output, "version: 1\nbackup: %q\n", command.Backup)
	case "migrate":
		if err := taskstate.MigrateLegacy(ctx, command.Root, command.Backup); err != nil {
			return fail(output, 1, err, "inspect the state and backup before retrying migration")
		}
		fmt.Fprintf(output, "version: 2\nbackup: %q\n", command.Backup)
	case "recover":
		if err := taskstate.RecoverLegacyMigration(ctx, command.Root, command.Backup); err != nil {
			return fail(output, 1, err, "inspect the backup and records before retrying recovery")
		}
		fmt.Fprintf(output, "version: 2\nbackup: %q\n", command.Backup)
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected.Command()), usage())
	}
	return 0
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout))
}
