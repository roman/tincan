package main

import (
	"context"
	"errors"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/supervise"
)

type observationRunner struct {
	output string
	err    error
	calls  [][]string
}

func (runner *observationRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, args)
	return []byte(runner.output), nil, runner.err
}

func TestObserveRequiresConfirmedWorkerIdentity(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		runErr      error
		wantMissing bool
		wantError   bool
	}{
		{name: "working", output: `{"result":{"agent":{"pane_id":"pane","agent_status":"working","agent":"codex"}}}`},
		{name: "unknown health preserves agent", output: `{"result":{"agent":{"pane_id":"pane","agent_status":"unknown","agent":"codex"}}}`},
		{name: "missing kind", output: `{"result":{"agent":{"pane_id":"pane","agent_status":"working"}}}`, wantError: true},
		{name: "different kind", output: `{"result":{"agent":{"pane_id":"pane","agent_status":"working","agent":"claude"}}}`, wantError: true},
		{name: "different pane", output: `{"result":{"agent":{"pane_id":"other","agent_status":"working","agent":"codex"}}}`, wantError: true},
		{name: "confirmed missing", output: `{"error":{"code":"agent_not_found","message":"gone"}}`, runErr: errors.New("exit 1"), wantMissing: true, wantError: true},
		{name: "command failure", output: `{"error":{"code":"server_unavailable","message":"down"}}`, runErr: errors.New("exit 1"), wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &observationRunner{output: tt.output, err: tt.runErr}
			worker := herdrWorker{client: herdr.Client{Runner: runner}, pane: "pane", name: "worker", kind: "codex"}
			err := worker.Observe(context.Background())
			if len(runner.calls) != 1 || len(runner.calls[0]) != 3 || runner.calls[0][2] != "worker" {
				t.Fatalf("lookup = %v, want worker name", runner.calls)
			}
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, want error %v", err, tt.wantError)
			}
			if errors.Is(err, supervise.ErrMissing) != tt.wantMissing {
				t.Fatalf("missing = %v, want %v", errors.Is(err, supervise.ErrMissing), tt.wantMissing)
			}
		})
	}
}
