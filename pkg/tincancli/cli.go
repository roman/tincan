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

const version = "0.1.0"

type cli struct {
	Root  string        `help:"Directory that stores mailboxes; defaults to .tincan in the current directory." env:"TINCAN_HOME"`
	Inbox InboxCommands `cmd:"" help:"Store, read, and acknowledge durable messages."`
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
	if err := runInbox(ctx, root, &command.Inbox, strings.TrimPrefix(selected, "inbox "), stdin, stdout); err != nil {
		return fail(stderr, "tincan", err)
	}
	return 0
}

// RunInbox preserves the standalone inbox parser while sharing its handlers.
func RunInbox(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var command struct {
		Root          string `help:"Directory that stores mailboxes." env:"TINCAN_HOME" required:""`
		InboxCommands `embed:""`
	}
	selected, code := parse(args, &command, "tincan-inbox", stdout, stderr)
	if code >= 0 {
		return code
	}
	if err := runInbox(ctx, command.Root, &command.InboxCommands, selected, stdin, stdout); err != nil {
		return fail(stderr, "tincan-inbox", err)
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
