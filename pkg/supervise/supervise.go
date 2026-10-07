package supervise

import (
	"context"
	"errors"
	"fmt"
	"time"

	cap "github.com/capatazlib/go-capataz/cap"
)

var (
	ErrMissing   = errors.New("worker missing")
	ErrProbe     = errors.New("worker probe failed")
	ErrRestart   = errors.New("worker restart failed")
	ErrRestarted = errors.New("worker restarted")
)

type Worker interface {
	Observe(context.Context) error
	Restart(context.Context) error
}

// Check restarts a worker only after its adapter confirms that it is missing.
func Check(ctx context.Context, worker Worker) error {
	err := worker.Observe(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrMissing) {
		return fmt.Errorf("%w: %v", ErrProbe, err)
	}
	if err := worker.Restart(ctx); err != nil {
		return fmt.Errorf("%w: %v", ErrRestart, err)
	}
	return ErrRestarted
}

type Config struct {
	Interval         time.Duration
	RestartTolerance int
	RestartWindow    time.Duration
}

// Run supervises a monitor goroutine. Capataz applies restart limits to failed checks.
func Run(ctx context.Context, worker Worker, config Config) error {
	if config.Interval <= 0 {
		return errors.New("interval must be positive")
	}
	if config.RestartTolerance < 0 || uint64(config.RestartTolerance) > 1<<32-1 || config.RestartWindow <= 0 {
		return errors.New("invalid restart tolerance")
	}
	monitor := cap.NewWorker("worker-health", func(workerCtx context.Context) error {
		ticker := time.NewTicker(config.Interval)
		defer ticker.Stop()
		if err := Check(workerCtx, worker); err != nil {
			return err
		}
		for {
			select {
			case <-workerCtx.Done():
				return nil
			case <-ticker.C:
				if err := Check(workerCtx, worker); err != nil {
					return err
				}
			}
		}
	}, cap.WithRestart(cap.Transient))
	spec := cap.NewSupervisorSpec("tincan", cap.WithNodes(monitor), cap.WithRestartTolerance(uint32(config.RestartTolerance), config.RestartWindow))
	supervisor, err := spec.Start(ctx)
	if err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() { result <- supervisor.Wait() }()
	select {
	case <-ctx.Done():
		return supervisor.Terminate()
	case err := <-result:
		return err
	}
}
