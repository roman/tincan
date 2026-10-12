package conciergecli

import (
	"context"
	"fmt"
	"io"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/taskstate"
)

type migrationTarget struct {
	Backup string `required:"" help:"Absolute sibling backup directory. Stop all state writers and controllers first."`
}

type stateCommands struct {
	Backup  migrationTarget `cmd:"" help:"Copy legacy state into a new backup directory; refuse an existing destination."`
	Migrate migrationTarget `cmd:"" help:"Convert legacy state into version two; verify and reuse an existing backup."`
	Recover migrationTarget `cmd:"" help:"Finish a pending migration using its verified backup."`
}

func (command stateCommands) run(ctx context.Context, root, selected string, output io.Writer) int {
	var target migrationTarget
	switch selected {
	case "state backup":
		target = command.Backup
	case "state migrate":
		target = command.Migrate
	case "state recover":
		target = command.Recover
	}
	if _, err := firstmate.ParseAbsolutePath(target.Backup); err != nil {
		return fail(output, 2, err, "use an absolute, clean --backup path")
	}
	var err error
	version := 2
	switch selected {
	case "state backup":
		_, err = taskstate.BackupLegacy(ctx, root, target.Backup)
		version = 1
	case "state migrate":
		err = taskstate.MigrateLegacy(ctx, root, target.Backup)
	case "state recover":
		err = taskstate.RecoverLegacyMigration(ctx, root, target.Backup)
	}
	if err != nil {
		return fail(
			output,
			1,
			err,
			"stop all state writers and controllers; verify the root and backup before retrying",
		)
	}
	if _, err := fmt.Fprintf(output, "version: %d\nbackup: %s\n", version, quote(target.Backup)); err != nil {
		return fail(
			output,
			1,
			err,
			"read the state before retrying; the operation completed but its output failed",
		)
	}
	return 0
}
