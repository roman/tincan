package notify

import (
	"errors"
	"testing"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantError error
	}{
		{name: "agent name", raw: "worker-1"},
		{name: "pane ID", raw: "w1:p2"},
		{name: "opaque adapter target", raw: "session/reviewer"},
		{name: "empty", wantError: ErrInvalidTarget},
		{name: "whitespace only", raw: " \t\n", wantError: ErrInvalidTarget},
		{name: "adapter punctuation", raw: ":w1:p2"},
		{name: "adapter path", raw: "team/worker"},
		{name: "adapter spaces", raw: "worker one"},
		{name: "newline", raw: "worker\nnext", wantError: ErrInvalidTarget},
		{name: "tab", raw: "worker\tnext", wantError: ErrInvalidTarget},
		{name: "NUL", raw: "worker\x00next", wantError: ErrInvalidTarget},
		{name: "Unicode", raw: "réviseur/一"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseTarget(test.raw)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("ParseTarget error = %v, want %v", err, test.wantError)
			}
			if err == nil && got.String() != test.raw {
				t.Fatalf("target = %q, want %q", got, test.raw)
			}
		})
	}
}
