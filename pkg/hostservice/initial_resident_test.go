package hostservice

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestInitialResidentRegistrationRetriesPreparationFailure(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	if err := registry.awaitInitialResident(key); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("temporary preparation failure")
	if _, err := registry.startInitialResident(context.Background(), key, endpoint, "binding", func(context.Context) (preparedController, error) { return preparedController{}, failure }); !errors.Is(
		err,
		failure,
	) {
		t.Fatalf("first registration: %v", err)
	}
	if len(registry.initialResidentTasks()) != 1 {
		t.Fatal("preparation failure lost the pending registration")
	}
	if _, err := registry.startInitialResident(context.Background(), key, endpoint, "binding", func(context.Context) (preparedController, error) {
		return preparedController{Run: func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil }}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(registry.initialResidentTasks()) != 0 {
		t.Fatal("ready resident remains pending")
	}
}

func TestInitialResidentStopRejectsPreviouslySelectedRetry(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	if err := registry.awaitInitialResident(key); err != nil {
		t.Fatal(err)
	}
	selected := registry.initialResidentTasks()
	if len(selected) != 1 {
		t.Fatal("registration was not selected")
	}
	if err := registry.stop(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.startInitialResident(context.Background(), selected[0], controllerEndpoint{session: "work", pane: "pane-1"}, "binding", func(context.Context) (preparedController, error) {
		t.Error("stopped retry reached preparation")
		return preparedController{}, nil
	}); !errors.Is(
		err,
		errInitialResidentStopped,
	) {
		t.Fatalf("stopped registration: %v", err)
	}
}

func TestInitialResidentFailureAfterReadinessDoesNotRearm(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	if err := registry.awaitInitialResident(key); err != nil {
		t.Fatal(err)
	}
	exit := make(chan struct{})
	var preparations atomic.Int32
	if _, err := registry.startInitialResident(context.Background(), key, endpoint, "binding", func(context.Context) (preparedController, error) {
		preparations.Add(1)
		return preparedController{Run: func(ctx context.Context, ready chan<- struct{}) error {
			close(ready)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-exit:
				return errors.New("restart budget exhausted")
			}
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	registry.mu.Lock()
	entry := registry.entries[key]
	registry.mu.Unlock()
	close(exit)
	<-entry.done
	if len(registry.initialResidentTasks()) != 0 {
		t.Fatal("failed active resident was queued for automatic registration")
	}
	if _, err := registry.startInitialResident(context.Background(), key, endpoint, "binding", func(context.Context) (preparedController, error) {
		preparations.Add(1)
		return preparedController{}, nil
	}); !errors.Is(
		err,
		errInitialResidentStopped,
	) {
		t.Fatalf("failed active registration rearmed: %v", err)
	}
	if preparations.Load() != 1 {
		t.Fatal("active failure reset the resident preparation")
	}
}

func TestInitialResidentCompletionRecordsReadinessBeforeReleasingEntry(t *testing.T) {
	for _, state := range []string{"ready", "not ready"} {
		t.Run(state, func(t *testing.T) {
			registry := newControllerRegistry(context.Background())
			defer registry.close()
			key := controllerKey{kind: taskController, id: "task-1"}
			endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
			if err := registry.awaitInitialResident(key); err != nil {
				t.Fatal(err)
			}
			entry := &controllerEntry{
				endpoint: endpoint,
				done:     make(chan struct{}),
				cancel:   func() {},
			}
			registry.entries[key] = entry
			registry.endpoints[endpoint] = struct{}{}
			ready := make(chan struct{})
			if state == "ready" {
				close(ready)
			}
			registry.finishRunning(key, entry, ready, nil)
			select {
			case <-entry.done:
			default:
				t.Fatal("completion did not release the entry")
			}
			_, err := registry.startInitialResident(
				context.Background(),
				key,
				endpoint,
				"binding",
				func(context.Context) (preparedController, error) {
					return preparedController{
						Run: func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
					}, nil
				},
			)
			if state == "ready" && !errors.Is(err, errInitialResidentStopped) {
				t.Fatalf("completed ready resident rearmed: %v", err)
			}
			if state == "not ready" && err != nil {
				t.Fatalf("unready failure stopped recovery: %v", err)
			}
		})
	}
}

func TestInitialResidentReadinessDuringCancellationDoesNotRearm(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	if err := registry.awaitInitialResident(key); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = registry.startInitialResident(
		ctx,
		key,
		controllerEndpoint{session: "work", pane: "pane-1"},
		"binding",
		func(context.Context) (preparedController, error) {
			return preparedController{
				Run: func(_ context.Context, ready chan<- struct{}) error { cancel(); close(ready); return context.Canceled },
			}, nil
		},
	)
	if len(registry.initialResidentTasks()) != 0 {
		t.Fatal("cancellation rearmed a resident that reported readiness")
	}
}
