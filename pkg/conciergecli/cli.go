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
)

const version = "0.1.0"

type cli struct {
	Root    string          `help:"Absolute Concierge state root; defaults to .concierge in the current directory." env:"CONCIERGE_HOME"`
	Backlog backlogCommands `cmd:"" help:"Queue and inspect authoritative task records."`
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
		kong.Exit(func(code int) { exitCode = code }),
	)
	if err != nil {
		return fail(stdout, 2, err, backlogHelp)
	}
	selected, err := parser.Parse(args)
	if exitCode >= 0 {
		return exitCode
	}
	if err != nil {
		return fail(stdout, 2, err, backlogHelp)
	}
	root := command.Root
	if root == "" {
		directory, err := os.Getwd()
		if err != nil {
			return fail(stdout, 1, err, "provide an absolute --root path")
		}
		root = filepath.Join(directory, ".concierge")
	}
	if _, err := firstmate.ParseAbsolutePath(root); err != nil {
		return fail(stdout, 2, err, "use an absolute, clean --root path")
	}
	return command.Backlog.run(ctx, root, selected.Command(), stdin, stdout)
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func fail(output io.Writer, code int, err error, help string) int {
	fmt.Fprintf(output, "error: %s\nhelp: %s\n", quote(err.Error()), quote(help))
	return code
}
