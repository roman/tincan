package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/primary"
)

func TestHookStatusOutputContract(t *testing.T) {
	for _, test := range []struct {
		name       string
		command    string
		event      string
		setup      string
		wantCode   int
		wantStatus string
	}{
		{name: "claim owner", command: "claim", event: "SessionStart", wantStatus: "owner"},
		{name: "claim held", command: "claim", event: "SessionStart", setup: "legacy owner", wantCode: 1, wantStatus: "held_by_other"},
		{name: "verify owner", command: "verify", event: "Stop", setup: "matching legacy owner", wantStatus: "owner"},
		{name: "release held", command: "release", event: "SessionEnd", setup: "legacy owner", wantCode: 1, wantStatus: "held_by_other"},
		{name: "release owner", command: "release", event: "SessionEnd", setup: "claimed owner", wantStatus: "vacant"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".concierge")
			identity := primary.Identity{
				Version: 2, Kind: "claude", SessionID: "current", PID: 1,
				StartTicks: "10", BootID: "boot", PIDNamespace: "pid:[1]",
			}
			switch test.setup {
			case "legacy owner", "matching legacy owner":
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
				owner := identity
				owner.Version = 0
				owner.PIDNamespace = ""
				if test.setup == "legacy owner" {
					owner.SessionID = "other"
				}
				data, err := json.Marshal(owner)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "primary.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			case "claimed owner":
				if _, err := primary.Claim(root, identity); err != nil {
					t.Fatal(err)
				}
			}
			input := strings.NewReader(fmt.Sprintf(
				`{"session_id":"current","cwd":%q,"hook_event_name":%q}`,
				filepath.Dir(root), test.event,
			))
			var output bytes.Buffer
			code := run([]string{"--root", root, test.command}, input, &output,
				func(primary.Hook) (primary.Identity, error) { return identity, nil })
			if code != test.wantCode {
				t.Fatalf("exit code = %d, output = %q; want %d", code, output.String(), test.wantCode)
			}
			captured := strings.TrimRight(output.String(), "\n")
			if want := "status: " + test.wantStatus + "\n"; !strings.HasPrefix(captured, want) {
				t.Fatalf("hook-visible output = %q; want prefix %q", captured, want)
			}
		})
	}
}

func TestRunRejectsHookOutsideRootBeforeResolvingProcess(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	input := strings.NewReader(
		`{"session_id":"s1","cwd":"/another/root","hook_event_name":"SessionStart"}`,
	)
	var output bytes.Buffer
	called := false
	status := run(
		[]string{"--root", root, "claim"},
		input,
		&output,
		func(primary.Hook) (primary.Identity, error) {
			called = true
			return primary.Identity{}, fmt.Errorf("unexpected resolver call")
		},
	)
	if status != 2 || called || !strings.Contains(output.String(), "cwd") {
		t.Fatalf("run() = %d, called=%t, output=%q", status, called, output.String())
	}
}

func TestRunRejectsWrongHookEvent(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	input := strings.NewReader(
		fmt.Sprintf(`{"session_id":"s1","cwd":%q,"hook_event_name":"Stop"}`, filepath.Dir(root)),
	)
	var output bytes.Buffer
	status := run(
		[]string{"--root", root, "claim"},
		input,
		&output,
		func(primary.Hook) (primary.Identity, error) {
			return primary.Identity{}, fmt.Errorf("unexpected resolver call")
		},
	)
	if status != 2 || !strings.Contains(output.String(), "SessionStart") {
		t.Fatalf("run() = %d, output=%q", status, output.String())
	}
}

func TestRunReleasesRecordedSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	identity := primary.Identity{
		Version: 2,
		Kind:    "codex", SessionID: "s1", PID: 1, StartTicks: "10",
		BootID: "boot", PIDNamespace: "pid:[1]",
	}
	resolve := func(primary.Hook) (primary.Identity, error) { return identity, nil }
	for _, check := range []struct {
		command string
		event   string
	}{
		{"claim", "SessionStart"},
		{"release", "SessionEnd"},
	} {
		input := strings.NewReader(fmt.Sprintf(
			`{"session_id":"s1","cwd":%q,"hook_event_name":%q}`,
			filepath.Dir(root), check.event,
		))
		var output bytes.Buffer
		if status := run([]string{"--root", root, check.command}, input, &output, resolve); status != 0 {
			t.Fatalf("%s returned %d: %s", check.command, status, output.String())
		}
	}
}
