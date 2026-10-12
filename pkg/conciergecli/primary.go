package conciergecli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/roman/tincan/pkg/primary"
)

const primaryHelp = "concierge primary claim|verify|release < HOOK_JSON; requires Linux /proc and a Claude or Codex lifecycle hook"

type primaryCommands struct {
	Claim   struct{} `cmd:"" help:"Claim primary ownership from a SessionStart hook; requires Linux /proc."`
	Verify  struct{} `cmd:"" help:"Verify primary ownership from a Stop hook; requires Linux /proc."`
	Release struct{} `cmd:"" help:"Release primary ownership from a SessionEnd hook; requires Linux /proc."`
}

func runPrimary(
	root, selected string,
	input io.Reader,
	output io.Writer,
	resolve func(primary.Hook) (primary.Identity, error),
) int {
	var hook primary.Hook
	if err := json.NewDecoder(input).Decode(&hook); err != nil {
		return fail(output, 2, err, "pass the hook JSON on stdin; "+primaryHelp)
	}
	if hook.Cwd != filepath.Dir(root) {
		return fail(
			output,
			2,
			fmt.Errorf("hook cwd does not match the Concierge root"),
			"launch Claude or Codex from the Exmachina root",
		)
	}
	expectedEvent := "SessionStart"
	switch selected {
	case "primary claim":
	case "primary verify":
		expectedEvent = "Stop"
	case "primary release":
		expectedEvent = "SessionEnd"
	default:
		return fail(output, 2, fmt.Errorf("unknown command %q", selected), primaryHelp)
	}
	if hook.HookEventName != expectedEvent {
		return fail(
			output,
			2,
			fmt.Errorf("%s needs a %s hook", selected, expectedEvent),
			primaryHelp,
		)
	}
	identity, err := resolve(hook)
	if err != nil {
		return fail(output, 1, err, primaryHelp)
	}
	var status primary.Status
	switch selected {
	case "primary claim":
		status, err = primary.Claim(root, identity)
	case "primary verify":
		status, err = primary.Verify(root, identity)
	case "primary release":
		status, err = primary.Release(root, identity)
	}
	if err != nil {
		return fail(output, 1, err, "inspect the Concierge primary state")
	}
	if _, err := fmt.Fprintf(output, "status: %s\nkind: %s\n", status, identity.Kind); err != nil {
		return 1
	}
	if status == primary.Owner || (selected == "primary release" && status == primary.Vacant) {
		return 0
	}
	if _, err := fmt.Fprintln(output, "help: another live session owns Concierge, or no primary owner is available"); err != nil {
		return 1
	}
	return 1
}
