package tincancli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/herdr"
)

func TestAdapterSelection(t *testing.T) {
	for _, test := range []struct {
		name        string
		args        []string
		wantAdapter string
		wantError   string
	}{
		{name: "default Herdr", wantAdapter: "herdr"},
		{name: "explicit Herdr", args: []string{"--adapter", "herdr"}, wantAdapter: "herdr"},
		{name: "unsupported adapter", args: []string{"--adapter", "tmux"}, wantAdapter: "tmux", wantError: "unsupported adapter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var options NotifyCommands
			parser, err := kong.New(&options)
			if err != nil {
				t.Fatal(err)
			}
			args := append(
				[]string{
					"--recipient",
					"worker",
					"--target",
					"worker",
					"--session",
					"work",
				},
				test.args...)
			if _, err := parser.Parse(args); err != nil {
				t.Fatal(err)
			}
			if options.Adapter != test.wantAdapter {
				t.Fatalf("adapter = %q, want %q", options.Adapter, test.wantAdapter)
			}
			bell, err := selectDoorbell(options.Adapter, options.Herdr, options.Session)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("selection error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := bell.(herdr.Client); !ok {
				t.Fatalf("selected doorbell type = %T", bell)
			}
		})
	}
}

func TestTargetOption(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		wantError bool
	}{
		{name: "missing", wantError: true},
		{name: "agent name", target: "agent-one"},
		{name: "pane ID", target: "w1:p2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var options NotifyCommands
			parser, err := kong.New(&options)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--recipient", "worker", "--session", "work"}
			if test.target != "" {
				args = append(args, "--target", test.target)
			}
			_, err = parser.Parse(args)
			if (err != nil) != test.wantError {
				t.Fatalf("Parse error = %v, wantError %v", err, test.wantError)
			}
			if err == nil && options.Target != test.target {
				t.Fatalf("target = %q, want %q", options.Target, test.target)
			}
		})
	}
}

func TestEmptyAmbientSessionRejected(t *testing.T) {
	t.Setenv("HERDR_SESSION", "")
	var options NotifyCommands
	parser, err := kong.New(&options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = parser.Parse(
		[]string{"--recipient", "worker", "--target", "worker"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selectDoorbell(options.Adapter, options.Herdr, options.Session); err == nil {
		t.Fatal("empty session was accepted")
	}
}

func TestDoorbellUsesAmbientHerdrSession(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "herdr")
	capture := filepath.Join(directory, "call")
	program := "#!/bin/sh\nprintf '%s\\n' \"$HERDR_SESSION\" \"$@\" > \"$TINCAN_TEST_CAPTURE\"\nprintf '{}\\n'\n"
	if err := os.WriteFile(executable, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_SESSION", "work")
	t.Setenv("TINCAN_TEST_CAPTURE", capture)
	var options NotifyCommands
	parser, err := kong.New(&options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse([]string{
		"--recipient", "worker", "--target", "worker", "--herdr", executable,
	}); err != nil {
		t.Fatal(err)
	}
	bell, err := selectDoorbell(options.Adapter, options.Herdr, options.Session)
	if err != nil {
		t.Fatal(err)
	}
	if err := bell.Prompt(context.Background(), "worker", "check inbox"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "work\nagent\nprompt\nworker\ncheck inbox\n" {
		t.Fatalf("Herdr call = %q", data)
	}
}
