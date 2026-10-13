package hostservice

import (
	"context"
	"errors"
	"github.com/roman/tincan/pkg/resident"
	"sync/atomic"
	"testing"
	"time"
)

func TestControllerRegistryKeepsResidentAcrossClientExit(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	caller, cancel := context.WithCancel(context.Background())
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	started := make(chan struct{})
	stopped := make(chan struct{})
	prepare := func(context.Context) (preparedController, error) {
		return preparedController{Run: func(ctx context.Context, ready chan<- struct{}) error {
			close(ready)
			close(started)
			<-ctx.Done()
			close(stopped)
			return nil
		}}, nil
	}
	already, err := registry.start(caller, key, endpoint, "binding", prepare)
	if err != nil || already {
		t.Fatalf("start: %v %v", already, err)
	}
	<-started
	cancel()
	select {
	case <-stopped:
		t.Fatal("client cancellation stopped resident")
	default:
	}
	already, err = registry.start(context.Background(), key, endpoint, "binding", prepare)
	if err != nil || !already {
		t.Fatalf("duplicate: %v %v", already, err)
	}
	if _, err := registry.start(context.Background(), controllerKey{kind: paneController, id: "pane-1"}, endpoint, "other", prepare); !errors.Is(
		err,
		errControllerBusy,
	) {
		t.Fatalf("overlap: %v", err)
	}
	if err := registry.stop(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("stop returned before resident joined")
	}
}

func TestControllerRegistryRetainsStoppingReservation(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	release := make(chan struct{})
	defer close(release)
	_, err := registry.start(
		context.Background(),
		key,
		endpoint,
		"binding",
		func(context.Context) (preparedController, error) {
			return preparedController{Run: func(ctx context.Context, ready chan<- struct{}) error {
				close(ready)
				<-ctx.Done()
				<-release
				return nil
			}}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := registry.stop(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop: %v", err)
	}
	if _, err := registry.acquire(endpoint); !errors.Is(err, errControllerBusy) {
		t.Fatalf("stopping reservation: %v", err)
	}
}

func TestControllerRegistryReleasesFailedPreparation(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	expected := errors.New("initial check failed")
	_, err := registry.start(
		context.Background(),
		key,
		endpoint,
		"binding",
		func(context.Context) (preparedController, error) { return preparedController{}, expected },
	)
	if !errors.Is(err, expected) {
		t.Fatalf("preparation: %v", err)
	}
	release, err := registry.acquire(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestControllerRegistryShutdownCancelsPreparation(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := registry.start(
			context.Background(),
			controllerKey{kind: taskController, id: "task-1"},
			controllerEndpoint{session: "work", pane: "pane-1"},
			"binding",
			func(ctx context.Context) (preparedController, error) {
				close(started)
				<-ctx.Done()
				return preparedController{}, ctx.Err()
			},
		)
		result <- err
	}()
	<-started
	done := make(chan struct{})
	go func() { registry.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join preparation")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation cancellation: %v", err)
	}
}

func TestControllerRegistryRejectsActivationAfterCancellation(t *testing.T) {
	for _, action := range []string{"stop", "close"} {
		t.Run(action, func(t *testing.T) {
			registry := newControllerRegistry(context.Background())
			defer registry.close()
			key := controllerKey{kind: taskController, id: "task-1"}
			root := t.TempDir()
			lease, err := resident.AcquireTaskLease(root, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			var ran atomic.Bool
			var closes atomic.Int32
			started := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				_, err := registry.start(
					context.Background(),
					key,
					controllerEndpoint{session: "work", pane: "pane-1"},
					"binding",
					func(ctx context.Context) (preparedController, error) {
						close(started)
						<-ctx.Done()
						return preparedController{
							Run:   func(context.Context, chan<- struct{}) error { ran.Store(true); return nil },
							Close: func() error { closes.Add(1); return lease.Close() },
						}, nil
					},
				)
				result <- err
			}()
			<-started
			if action == "stop" {
				if err := registry.stop(context.Background(), key); err != nil {
					t.Fatal(err)
				}
			} else {
				registry.close()
			}
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled activation reported success: %v", err)
			}
			if ran.Load() || closes.Load() != 1 {
				t.Fatalf("runner started=%v cleanup count=%d", ran.Load(), closes.Load())
			}
			replacement, err := resident.AcquireTaskLease(root, "task-1")
			if err != nil {
				t.Fatalf("cleanup retained lease: %v", err)
			}
			if err := replacement.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestControllerRegistryStopWaitsForCleanup(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	cleaning := make(chan struct{})
	release := make(chan struct{})
	_, err := registry.start(
		context.Background(),
		key,
		endpoint,
		"binding",
		func(context.Context) (preparedController, error) {
			return preparedController{
				Run:   func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
				Close: func() error { close(cleaning); <-release; return nil },
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- registry.stop(context.Background(), key) }()
	<-cleaning
	_, reservationError := registry.acquire(endpoint)
	select {
	case <-stopped:
		close(release)
		t.Fatal("stop returned before cleanup")
	default:
	}
	close(release)
	if !errors.Is(reservationError, errControllerBusy) {
		t.Fatalf("cleanup reservation: %v", reservationError)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func TestControllerRegistryRejectsExpiredStartBeforePreparation(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := registry.start(
		ctx,
		controllerKey{kind: taskController, id: "task-1"},
		controllerEndpoint{session: "work", pane: "pane-1"},
		"binding",
		func(context.Context) (preparedController, error) {
			t.Error("expired start reached preparation")
			return preparedController{
				Run: func(context.Context, chan<- struct{}) error { return nil },
			}, nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expired start: %v", err)
	}
}

func TestControllerRegistryRejectsExitBeforeReadiness(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	expected := errors.New("watch setup failed")
	_, err := registry.start(
		context.Background(),
		key,
		endpoint,
		"binding",
		func(context.Context) (preparedController, error) {
			return preparedController{
				Run: func(context.Context, chan<- struct{}) error { return expected },
			}, nil
		},
	)
	if !errors.Is(err, expected) {
		t.Fatalf("startup error: %v", err)
	}
	release, err := registry.acquire(endpoint)
	if err != nil {
		t.Fatalf("failed startup retained reservation: %v", err)
	}
	release()
}

func TestControllerRegistryWaitsForReadinessAndJoinsCanceledStartup(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	started := make(chan struct{})
	cleaned := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := registry.start(
			caller,
			key,
			endpoint,
			"binding",
			func(context.Context) (preparedController, error) {
				return preparedController{
					Run: func(ctx context.Context, ready chan<- struct{}) error {
						close(started)
						<-ctx.Done()
						return ctx.Err()
					},
					Close: func() error { close(cleaned); return nil },
				}, nil
			},
		)
		result <- err
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("start returned before readiness: %v", err)
	default:
	}
	if _, err := registry.start(context.Background(), key, endpoint, "binding", nil); !errors.Is(
		err,
		errControllerBusy,
	) {
		t.Fatalf("starting duplicate: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled startup: %v", err)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("startup returned before cleanup")
	}
	release, err := registry.acquire(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestControllerRegistryQuiesceReservesTaskAndEndpointThroughOperation(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	cleaning := make(chan struct{})
	allowCleanup := make(chan struct{})
	prepare := func(context.Context) (preparedController, error) {
		return preparedController{
			Run:   func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
			Close: func() error { close(cleaning); <-allowCleanup; return nil },
		}, nil
	}
	if _, err := registry.start(context.Background(), key, endpoint, "binding", prepare); err != nil {
		t.Fatal(err)
	}
	result := make(chan func(), 1)
	failed := make(chan error, 1)
	go func() {
		release, err := registry.quiesce(context.Background(), key, endpoint)
		if err != nil {
			failed <- err
			return
		}
		result <- release
	}()
	<-cleaning
	assertReserved := func() {
		t.Helper()
		if _, err := registry.start(context.Background(), key, controllerEndpoint{session: "other", pane: "pane-2"}, "changed", nil); !errors.Is(
			err,
			errControllerBusy,
		) {
			t.Fatalf("task reservation: %v", err)
		}
		if _, err := registry.acquire(endpoint); !errors.Is(err, errControllerBusy) {
			t.Fatalf("endpoint reservation: %v", err)
		}
	}
	assertReserved()
	select {
	case <-result:
		t.Fatal("operation began before cleanup")
	default:
	}
	close(allowCleanup)
	var release func()
	select {
	case release = <-result:
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("quiesce did not join")
	}
	assertReserved()
	release()
	release()
	if _, err := registry.start(context.Background(), key, endpoint, "binding", func(context.Context) (preparedController, error) {
		return preparedController{Run: func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil }}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestControllerRegistryCanceledQuiesceJoinsCleanupBeforeRelease(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	cleaning := make(chan struct{})
	allowCleanup := make(chan struct{})
	_, err := registry.start(
		context.Background(),
		key,
		endpoint,
		"binding",
		func(context.Context) (preparedController, error) {
			return preparedController{
				Run:   func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
				Close: func() error { close(cleaning); <-allowCleanup; return nil },
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		release, err := registry.quiesce(caller, key, endpoint)
		if release != nil {
			release()
		}
		result <- err
	}()
	<-cleaning
	cancel()
	if _, err := registry.acquire(endpoint); !errors.Is(err, errControllerBusy) {
		t.Fatalf("canceled operation released before cleanup: %v", err)
	}
	select {
	case err := <-result:
		t.Fatalf("returned before cleanup: %v", err)
	default:
	}
	close(allowCleanup)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled operation: %v", err)
	}
	release, err := registry.acquire(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
	operationRelease, err := registry.quiesce(context.Background(), key, endpoint)
	if err != nil {
		t.Fatalf("cancellation retained task reservation: %v", err)
	}
	operationRelease()
}

func TestControllerRegistryQuiesceRejectsUnrelatedResident(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	_, err := registry.start(
		context.Background(),
		controllerKey{kind: paneController, id: "pane-1"},
		endpoint,
		"manual",
		func(context.Context) (preparedController, error) {
			return preparedController{
				Run: func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := registry.quiesce(context.Background(), controllerKey{kind: taskController, id: "task-1"}, endpoint); !errors.Is(
		err,
		errControllerBusy,
	) {
		if release != nil {
			release()
		}
		t.Fatalf("unrelated resident: %v", err)
	}
}

func TestAdoptionReservationPreservesMatchingTaskResident(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	stopped := make(chan struct{})
	prepare := func(context.Context) (preparedController, error) {
		return preparedController{
			Run:   func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
			Close: func() error { close(stopped); return nil },
		}, nil
	}
	if _, err := registry.start(context.Background(), key, endpoint, "binding", prepare); err != nil {
		t.Fatal(err)
	}
	release, err := registry.reserveAdoption(key, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
		t.Fatal("adoption canceled resident")
	default:
	}
	if _, err := registry.quiesce(context.Background(), key, endpoint); !errors.Is(
		err,
		errControllerBusy,
	) {
		t.Fatalf("adoption allowed concurrent stop: %v", err)
	}
	if err := registry.stop(context.Background(), key); !errors.Is(err, errControllerBusy) {
		t.Fatalf("adoption allowed resident stop: %v", err)
	}
	if _, err := registry.reserveAdoption(key, controllerEndpoint{session: "other", pane: "pane-2"}); !errors.Is(
		err,
		errControllerBusy,
	) {
		t.Fatalf("adoption escaped task reservation: %v", err)
	}
	release()
	release()
	if already, err := registry.start(context.Background(), key, endpoint, "binding", prepare); err != nil ||
		!already {
		t.Fatalf("adoption released resident: %v %v", already, err)
	}
}

func TestAdoptionReservationRejectsManualResident(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	endpoint := controllerEndpoint{session: "work", pane: "pane-1"}
	_, err := registry.start(
		context.Background(),
		controllerKey{kind: paneController, id: "pane-1"},
		endpoint,
		"manual",
		func(context.Context) (preparedController, error) {
			return preparedController{
				Run: func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.reserveAdoption(controllerKey{kind: taskController, id: "task-1"}, endpoint); !errors.Is(
		err,
		errControllerBusy,
	) {
		t.Fatalf("manual overlap: %v", err)
	}
}
