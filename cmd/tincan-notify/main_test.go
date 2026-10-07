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
			args := append([]string{"--root", t.TempDir(), "--recipient", "worker"}, test.args...)
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
