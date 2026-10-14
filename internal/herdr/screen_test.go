package herdr

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestVisibleScreenIsBoundedAndReadOnly(t *testing.T) {
	for _, test := range []struct {
		name, pane, body string
		failure          error
		wantError        bool
	}{
		{name: "current text", pane: "p1", body: "Accessing workspace:\nYes, I trust this folder\n"},
		{name: "oversized", pane: "p1", body: strings.Repeat("x", 64*1024+1), wantError: true},
		{name: "invalid pane", pane: "bad pane", wantError: true},
		{name: "read failed", pane: "p1", failure: errors.New("exit 1"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{
				responses: []runResponse{{stdout: []byte(test.body), err: test.failure}},
			}
			client := Client{Runner: runner}
			screen, err := client.VisibleScreen(context.Background(), test.pane)
			if (err != nil) != test.wantError {
				t.Fatalf("screen = %q, %v", screen, err)
			}
			if !test.wantError && screen != test.body {
				t.Fatalf("screen text changed: %q", screen)
			}
			if test.name == "invalid pane" {
				if len(runner.calls) != 0 {
					t.Fatal("invalid target accessed Herdr")
				}
				return
			}
			want := [][]string{
				{"agent", "read", "p1", "--source", "visible", "--lines", "80", "--format", "text"},
			}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Fatalf("diagnostic commands = %v", runner.calls)
			}
		})
	}
}
