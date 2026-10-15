package tincancli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
)

// Release builds set version from VERSION through the Go linker.
var version = "dev"

type cli struct {
	Root   string         `help:"Directory that stores mailboxes; defaults to .tincan in the current directory." env:"TINCAN_HOME"`
	Notify NotifyCommands `cmd:"" help:"Ring a generic endpoint while its inbox has messages."`
	Inbox  InboxCommands  `cmd:"" help:"Store, read, and acknowledge durable messages."`
}

// Run executes the merged generic transport command without exiting the process.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if printVersion(args, stdout) {
		return 0
	}
	var command cli
	selected, code := parse(args, &command, "tincan", stdout, stderr)
	if code >= 0 {
		return code
	}
	root := command.Root
	if root == "" {
		directory, err := os.Getwd()
		if err != nil {
			return fail(stderr, "tincan", err)
		}
		root = filepath.Join(directory, ".tincan")
	}
	if selected == "notify" {
		report := func(err error) { fmt.Fprintln(stderr, "tincan:", err) }
		if err := runNotify(ctx, root, &command.Notify, report); err != nil {
			return fail(stderr, "tincan", err)
		}
		return 0
	}
	if err := runInbox(ctx, root, &command.Inbox, strings.TrimPrefix(selected, "inbox "), stdin, stdout); err != nil {
		return fail(stderr, "tincan", err)
	}
	return 0
}

func parse(args []string, model any, name string, stdout, stderr io.Writer) (string, int) {
	exitCode := -1
	parser, err := kong.New(
		model,
		kong.Name(name),
		kong.Description("A durable mailbox for independent agents."),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { exitCode = code }),
	)
	if err != nil {
		return "", fail(stderr, name, err)
	}
	selected, err := parser.Parse(args)
	if exitCode >= 0 {
		return "", exitCode
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return "", 2
	}
	return selected.Command(), -1
}

func printVersion(args []string, stdout io.Writer) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--version", "-v", "-V":
		fmt.Fprintln(stdout, version)
		return true
	}
	return false
}

func fail(stderr io.Writer, name string, err error) int {
	fmt.Fprintf(stderr, "%s: %v\n", name, err)
	return 1
}
