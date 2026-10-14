package conciergecli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/primary"
)

func primaryTestIdentity() primary.Identity {
	return primary.Identity{
		Version:      2,
		Kind:         "claude",
		SessionID:    "current",
		PID:          1,
		StartTicks:   "10",
		BootID:       "test-boot",
		PIDNamespace: "pid:[1]",
	}
}

func primaryHookInput(root, event string) string {
	return fmt.Sprintf(
		`{"session_id":"current","cwd":%q,"hook_event_name":%q,"extra":"accepted"}`,
		filepath.Dir(root),
		event,
	)
}

func TestPrimaryHookOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, command, event, setup, status string
		code                                int
	}{
		{"claim owner", "claim", "SessionStart", "", "owner", 0},
		{"claim secondary", "claim", "SessionStart", "other", "held_by_other", 1},
		{"verify owner", "verify", "Stop", "matching", "owner", 0},
		{"verify secondary", "verify", "Stop", "other", "held_by_other", 1},
		{"verify vacant", "verify", "Stop", "", "vacant", 1},
		{"verify stale", "verify", "Stop", "stale", "stale", 1},
		{"release owner", "release", "SessionEnd", "matching", "vacant", 0},
		{"release secondary", "release", "SessionEnd", "other", "held_by_other", 1},
		{"release vacant", "release", "SessionEnd", "", "vacant", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".concierge")
			identity := primaryTestIdentity()
			if test.setup != "" {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				owner := identity
				if test.setup != "stale" {
					owner.Version = 0
					owner.PIDNamespace = ""
				}
				if test.setup == "other" {
					owner.SessionID = "other"
				}
				data, _ := json.Marshal(owner)
				if err := os.WriteFile(filepath.Join(root, "primary.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			code := runPrimary(
				root,
				"primary "+test.command,
				strings.NewReader(primaryHookInput(root, test.event)),
				&output,
				func(hook primary.Hook) (primary.Identity, error) {
					if hook.SessionID != "current" {
						t.Fatalf("hook: %#v", hook)
					}
					return identity, nil
				},
			)
			prefix := "status: " + test.status + "\nkind: claude\n"
			if code != test.code || !strings.HasPrefix(output.String(), prefix) {
				t.Fatalf("outcome: %d %s", code, &output)
			}
			if test.code == 0 && output.String() != prefix {
				t.Fatalf("success output: %s", &output)
			}
			if _, err := os.Stat(filepath.Join(root, ".task-state-version")); !os.IsNotExist(err) {
				t.Fatalf("primary initialized task state: %v", err)
			}
		})
	}
}

func TestPrimaryRejectsHookBeforeIdentityResolution(t *testing.T) {
	for _, invalid := range []string{"json", "cwd", "event"} {
		t.Run(invalid, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".concierge")
			input := primaryHookInput(root, "SessionStart")
			switch invalid {
			case "json":
				input = "{"
			case "cwd":
				input = strings.Replace(input, filepath.Dir(root), "/wrong", 1)
			case "event":
				input = primaryHookInput(root, "Stop")
			}
			called := false
			var output bytes.Buffer
			code := runPrimary(
				root,
				"primary claim",
				strings.NewReader(input),
				&output,
				func(primary.Hook) (primary.Identity, error) { called = true; return primaryTestIdentity(), nil },
			)
			if code != 2 || called || !strings.Contains(output.String(), "error:") {
				t.Fatalf("validation: %d called=%v %s", code, called, &output)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("created state before validation: %v", err)
			}
		})
	}
}

func TestPrimaryResolverAndWriterErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	var output bytes.Buffer
	code := runPrimary(
		root,
		"primary claim",
		strings.NewReader(primaryHookInput(root, "SessionStart")),
		&output,
		func(primary.Hook) (primary.Identity, error) {
			return primary.Identity{}, errors.New("identity unavailable")
		},
	)
	if code != 1 || !strings.Contains(output.String(), "identity unavailable") {
		t.Fatalf("resolver: %d %s", code, &output)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("created state before resolution: %v", err)
	}
	code = runPrimary(
		root,
		"primary claim",
		strings.NewReader(primaryHookInput(root, "SessionStart")),
		failedOutput{},
		func(primary.Hook) (primary.Identity, error) { return primaryTestIdentity(), nil },
	)
	if code != 1 {
		t.Fatalf("writer: %d", code)
	}
}

func TestPrimaryMergedRoutingAndRootSelection(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	explicit := filepath.Join(t.TempDir(), "explicit")
	environment := filepath.Join(t.TempDir(), "environment")
	for _, test := range []struct {
		name, env, root string
		args            []string
	}{
		{"explicit", environment, explicit, []string{"--root", explicit}},
		{"environment", environment, environment, nil},
		{"default", "", filepath.Join(cwd, ".concierge"), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CONCIERGE_HOME", test.env)
			args := append(
				append([]string{}, test.args...),
				"--recipient",
				"../unused",
				"--message-root",
				"relative-unused",
				"primary",
				"claim",
			)
			var output bytes.Buffer
			// A matching cwd reaches event validation before the real process resolver.
			code := Run(
				context.Background(),
				args,
				strings.NewReader(primaryHookInput(test.root, "Stop")),
				&output,
				io.Discard,
			)
			if code != 2 || !strings.Contains(output.String(), "SessionStart") ||
				strings.Contains(output.String(), "cwd") {
				t.Fatalf("root routing: %d %s", code, &output)
			}
		})
	}
	var help bytes.Buffer
	if code := Run(context.Background(), []string{"primary", "claim", "--help"}, strings.NewReader(""), &help, io.Discard); code != 0 ||
		!strings.Contains(help.String(), "native process") {
		t.Fatalf("platform help: %d %s", code, &help)
	}
}
