package resident

import (
	"context"
	"errors"

	"github.com/roman/tincan/pkg/supervise"
)

// Controller runs process health supervision and event-driven inbox delivery.
type Controller struct {
	Watcher     Watcher
	Worker      supervise.Worker
	Supervision supervise.Config
}

func (controller Controller) Run(ctx context.Context, ready chan<- struct{}) error {
	if controller.Worker == nil {
		return errors.New("resident controller needs a worker")
	}
	childContext, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- supervise.Run(childContext, controller.Worker, controller.Supervision) }()
	go func() { results <- controller.Watcher.Run(childContext, ready) }()
	select {
	case <-ctx.Done():
		cancel()
		return errors.Join(<-results, <-results)
	case first := <-results:
		cancel()
		return errors.Join(first, <-results)
	}
}
