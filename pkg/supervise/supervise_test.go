package supervise

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeWorker struct {
	observeErr error
	restartErr error
	restarts   int
}

func (worker *fakeWorker) Observe(context.Context) error { return worker.observeErr }

func (worker *fakeWorker) Restart(context.Context) error {
	worker.restarts++
	return worker.restartErr
}

func TestCheckRestartsOnlyMissingWorker(t *testing.T) {
	tests := []struct {
		name         string
		observeErr   error
		restartErr   error
		wantRestarts int
		wantErr      error
	}{
		{name: "healthy"},
		{name: "probe error", observeErr: errors.New("transport failed"), wantErr: ErrProbe},
		{name: "missing", observeErr: ErrMissing, wantRestarts: 1, wantErr: ErrRestarted},
		{
			name:         "restart failure",
			observeErr:   ErrMissing,
			restartErr:   errors.New("cannot start"),
			wantRestarts: 1,
			wantErr:      ErrRestart,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := &fakeWorker{observeErr: tt.observeErr, restartErr: tt.restartErr}
			err := Check(context.Background(), worker)
			if worker.restarts != tt.wantRestarts {
				t.Fatalf("restarts = %d, want %d", worker.restarts, tt.wantRestarts)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestRunStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &fakeWorker{}
	result := make(chan error, 1)
	go func() {
		result <- Run(ctx, worker, Config{Interval: time.Millisecond, RestartTolerance: 1, RestartWindow: time.Second})
	}()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunStopsAfterRestartTolerance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	worker := &fakeWorker{observeErr: errors.New("probe unavailable")}
	err := Run(
		ctx,
		worker,
		Config{Interval: time.Millisecond, RestartTolerance: 0, RestartWindow: time.Second},
	)
	if err == nil {
		t.Fatal("Run returned nil after monitor failure")
	}
}

func TestRunCountsSuccessfulExternalRestarts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	worker := &fakeWorker{observeErr: ErrMissing}
	err := Run(
		ctx,
		worker,
		Config{Interval: time.Millisecond, RestartTolerance: 2, RestartWindow: time.Second},
	)
	if err == nil {
		t.Fatal("Run returned nil after repeated external restarts")
	}
	if worker.restarts != 3 {
		t.Fatalf("restarts = %d, want 3", worker.restarts)
	}
}
