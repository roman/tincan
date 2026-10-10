package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/primary"
)

type cli struct {
	Root   string   `help:"Absolute Concierge state root." required:""`
	Claim  struct{} `cmd:"" help:"Claim the primary role from a SessionStart hook."`
	Verify struct{} `cmd:"" help:"Verify the primary role from a Stop hook."`
}

func help() string {
	return "concierge-primary --root ROOT claim|verify < HOOK_JSON"
}

func fail(output io.Writer, code int, err error, next string) int {
	fmt.Fprintf(output, "error: %q\nhelp: %s\n", err, next)
	return code
}

func run(
	arguments []string,
	input io.Reader,
	output io.Writer,
	resolve func(primary.Hook) (primary.Identity, error),
) int {
	var command cli
	parser, err := kong.New(
		&command,
		kong.Name("concierge-primary"),
		kong.Writers(output, io.Discard),
	)
	if err != nil {
		return fail(output, 2, err, help())
	}
	selected, err := parser.Parse(arguments)
	if err != nil {
		return fail(output, 2, err, help())
	}
	if !filepath.IsAbs(command.Root) || filepath.Clean(command.Root) != command.Root {
		return fail(output, 2, fmt.Errorf("--root must be an absolute, clean path"), help())
	}
	var hook primary.Hook
	if err := json.NewDecoder(input).Decode(&hook); err != nil {
		return fail(output, 2, err, "pass the hook JSON on stdin")
	}
	if hook.Cwd != filepath.Dir(command.Root) {
		return fail(output, 2, fmt.Errorf("hook cwd does not match the Concierge root"),
			"launch Claude or Codex from the Exmachina root")
	}
	expectedEvent := "SessionStart"
	if selected.Command() == "verify" {
		expectedEvent = "Stop"
	}
	if hook.HookEventName != expectedEvent {
		return fail(
			output,
			2,
			fmt.Errorf("%s needs a %s hook", selected.Command(), expectedEvent),
			help(),
		)
	}
	identity, err := resolve(hook)
	if err != nil {
		return fail(output, 1, err, "run this command from a Claude or Codex lifecycle hook")
	}
	var status primary.Status
	if selected.Command() == "claim" {
		status, err = primary.Claim(command.Root, identity)
	} else {
		status, err = primary.Verify(command.Root, identity)
	}
	if err != nil {
		return fail(output, 1, err, "inspect the Concierge primary state")
	}
	fmt.Fprintf(output, "status: %s\nkind: %s\n", status, identity.Kind)
	if status == primary.Owner {
		return 0
	}
	fmt.Fprintln(
		output,
		"help: another live session owns Concierge, or no primary owner is available",
	)
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, primary.CurrentHookIdentity))
}
