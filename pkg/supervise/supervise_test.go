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

type cancellationWorker struct {
	entered       chan struct{}
	healthyChecks int
}

func (worker *cancellationWorker) Observe(ctx context.Context) error {
	if worker.healthyChecks > 0 {
		worker.healthyChecks--
		return nil
	}
	close(worker.entered)
	<-ctx.Done()
	return ctx.Err()
}

func (worker *cancellationWorker) Restart(context.Context) error {
	return errors.New("canceled probe must not restart a worker")
}

func TestRunCancellationDuringProbe(t *testing.T) {
	for _, test := range []struct {
		name          string
		healthyChecks int
	}{{"initial", 0}, {"periodic", 1}} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			worker := &cancellationWorker{
				entered:       make(chan struct{}),
				healthyChecks: test.healthyChecks,
			}
			result := make(chan error, 1)
			go func() {
				result <- Run(ctx, worker, Config{Interval: time.Millisecond, RestartTolerance: 0, RestartWindow: time.Second})
			}()
			select {
			case <-worker.entered:
			case <-time.After(time.Second):
				t.Fatal("probe did not start")
			}
			cancel()
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("canceled probe returned %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled probe did not stop")
			}
		})
	}
}

func TestRunAlreadyCanceledSkipsProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker := &cancellationWorker{entered: make(chan struct{})}
	if err := Run(ctx, worker, Config{Interval: time.Second, RestartWindow: time.Second}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-worker.entered:
		t.Fatal("already canceled run started a probe")
	default:
	}
	if err := Run(ctx, worker, Config{}); err == nil {
		t.Fatal("cancellation bypassed invalid configuration")
	}
}
