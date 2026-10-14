package primary

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestClassifyExecutable(t *testing.T) {
	for _, check := range []struct{ path, want string }{
		{"/nix/store/hash-claude-code-2.1.292/bin/.claude-wrapped", "claude"},
		{"/home/user/.claude/versions/2.1.292", "claude"},
		{"/nix/store/hash-codex-0.161/bin/.codex-wrapped", "codex"},
		{"/nix/store/hash-codex-0.161/bin/.codex-wrapped (deleted)", "codex"},
		{"/nix/store/hash-claude-code-2.1.292/bin/.claude-wrapped (deleted)", "claude"},
		{"/tmp/codex-fake", ""},
		{"/tmp/claude-code-fake", ""},
	} {
		if got := classifyExecutable(check.path); got != check.want {
			t.Errorf("classifyExecutable(%q) = %q, want %q", check.path, got, check.want)
		}
	}
}

func TestResolveHookIdentity(t *testing.T) {
	processes := map[int]Process{
		10: {PID: 10, ParentPID: 20, Kind: "", StartTicks: "10"},
		20: {PID: 20, ParentPID: 30, Kind: "claude", StartTicks: "20"},
		30: {PID: 30, ParentPID: 1, Kind: "codex", StartTicks: "30"},
	}
	lookup := func(pid int) (Process, error) { return processes[pid], nil }
	checks := []struct {
		name      string
		payload   Hook
		env       map[string]string
		wantKind  string
		wantPID   int
		parentPID int
		wantError bool
	}{
		{"Claude under Codex", Hook{SessionID: "claude-session"}, map[string]string{
			"CLAUDE_CODE_SESSION_ID": "claude-session", "CLAUDE_PID": "20", "CODEX_SESSION_ID": "codex-session",
		}, "claude", 20, 10, false},
		{"Claude PID mismatch", Hook{SessionID: "claude-session"}, map[string]string{
			"CLAUDE_CODE_SESSION_ID": "claude-session", "CLAUDE_PID": "30",
		}, "", 0, 10, true},
		{"Codex", Hook{SessionID: "codex-session"}, map[string]string{
			"CODEX_SESSION_ID": "codex-session",
		}, "codex", 30, 30, false},
		{"Codex with inherited session", Hook{SessionID: "codex-session"}, map[string]string{
			"CODEX_SESSION_ID": "parent-session",
		}, "codex", 30, 30, false},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			identity, err := ResolveHookIdentity(
				check.payload,
				check.env,
				check.parentPID,
				lookup,
				"boot",
				"pid:[1]",
			)
			if (err != nil) != check.wantError {
				t.Fatalf("ResolveHookIdentity() error = %v, wantError %t", err, check.wantError)
			}
			if err == nil &&
				(identity.Version != 2 || identity.Kind != check.wantKind || identity.PID != check.wantPID ||
					identity.SessionID != check.payload.SessionID ||
					identity.BootID != "boot" || identity.PIDNamespace != "pid:[1]") {
				t.Fatalf("ResolveHookIdentity() = %+v", identity)
			}
		})
	}
}

func TestReadProcessReportsExitedProcessMissing(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^$")
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	_, err := readProcess(command.ProcessState.Pid())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exited process lookup = %v, want missing process", err)
	}
}
