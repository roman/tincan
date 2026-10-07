package main

import (
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
			var options cli
			parser, err := kong.New(&options)
			if err != nil {
				t.Fatal(err)
			}
			args := append(
				[]string{"--root", t.TempDir(), "--recipient", "worker", "--target", "worker"},
				test.args...)
			if _, err := parser.Parse(args); err != nil {
				t.Fatal(err)
			}
			if options.Adapter != test.wantAdapter {
				t.Fatalf("adapter = %q, want %q", options.Adapter, test.wantAdapter)
			}
			bell, err := selectDoorbell(options.Adapter, options.Herdr)
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
			var options cli
			parser, err := kong.New(&options)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"--root", t.TempDir(), "--recipient", "worker"}
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
