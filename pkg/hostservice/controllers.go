package hostservice

import (
	"context"
	"errors"
	"sync"
)

type controllerKind string

const (
	taskController controllerKind = "task"
	paneController controllerKind = "pane"
)

type controllerStoppedError struct{ cause error }

func (failure *controllerStoppedError) Error() string { return failure.cause.Error() }
func (failure *controllerStoppedError) Unwrap() error { return failure.cause }

var errControllerBusy = errors.New("controller or endpoint is already reserved")

type controllerKey struct {
	kind controllerKind
	id   string
}
type controllerEndpoint struct{ session, pane string }
type controllerRun func(context.Context, chan<- struct{}) error

// Successful preparation transfers cleanup ownership to the registry.
// Failed preparation must release its own resources.
type preparedController struct {
	Run   controllerRun
	Close func() error
}

func (prepared preparedController) close() error {
	if prepared.Close == nil {
		return nil
	}
	return prepared.Close()
}

type controllerEntry struct {
	endpoint      controllerEndpoint
	specification string
	cancel        context.CancelFunc
	done          chan struct{}
	active        bool
	stopping      bool
	cleanupError  error
	terminalError error
	exited        bool
}

type controllerRegistry struct {
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	entries            map[controllerKey]*controllerEntry
	endpoints          map[controllerEndpoint]struct{}
	closed             bool
	operationKeys      map[controllerKey]struct{}
	operationEndpoints map[controllerEndpoint]struct{}
	report             func(error)
}

func newControllerRegistry(ctx context.Context) *controllerRegistry {
	lifetime, cancel := context.WithCancel(ctx)
	return &controllerRegistry{
		ctx:                lifetime,
		cancel:             cancel,
		entries:            make(map[controllerKey]*controllerEntry),
		endpoints:          make(map[controllerEndpoint]struct{}),
		operationKeys:      make(map[controllerKey]struct{}),
		operationEndpoints: make(map[controllerEndpoint]struct{}),
	}
}

func (registry *controllerRegistry) start(
	ctx context.Context,
	key controllerKey,
	endpoint controllerEndpoint,
	specification string,
	prepare func(context.Context) (preparedController, error),
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	registry.mu.Lock()
	if registry.closed || registry.ctx.Err() != nil {
		registry.mu.Unlock()
		return false, context.Canceled
	}
	if _, exists := registry.operationKeys[key]; exists {
		registry.mu.Unlock()
		return false, errControllerBusy
	}
	if _, exists := registry.operationEndpoints[endpoint]; exists {
		registry.mu.Unlock()
		return false, errControllerBusy
	}
	if existing := registry.entries[key]; existing != nil {
		identical := existing.active && !existing.stopping && existing.endpoint == endpoint &&
			existing.specification == specification
		registry.mu.Unlock()
		if identical {
			return true, nil
		}
		return false, errControllerBusy
	}
	if _, exists := registry.endpoints[endpoint]; exists {
		registry.mu.Unlock()
		return false, errControllerBusy
	}
	lifetime, cancel := context.WithCancel(registry.ctx)
	entry := &controllerEntry{
		endpoint:      endpoint,
		specification: specification,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	registry.entries[key] = entry
	registry.endpoints[endpoint] = struct{}{}
	registry.mu.Unlock()

	preparation, cancelPreparation := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(lifetime, cancelPreparation)
	prepared, err := prepare(preparation)
	stopCancellation()
	cancelPreparation()
	if err != nil {
		registry.finish(key, entry, nil)
		return false, err
	}
	if prepared.Run == nil {
		cleanupError := prepared.close()
		registry.finish(key, entry, cleanupError)
		return false, errors.Join(
			errors.New("controller preparation returned no runner"),
			cleanupError,
		)
	}
	registry.mu.Lock()
	if registry.closed || entry.stopping || lifetime.Err() != nil {
		registry.mu.Unlock()
		cleanupError := prepared.close()
		registry.finish(key, entry, cleanupError)
		return false, errors.Join(context.Canceled, cleanupError)
	}
	registry.mu.Unlock()
	ready := make(chan struct{})
	go func() {
		runError := prepared.Run(lifetime, ready)
		cleanupError := prepared.close()
		registry.mu.Lock()
		active := entry.active
		entry.terminalError = errors.Join(runError, cleanupError)
		entry.exited = true
		registry.mu.Unlock()
		registry.finish(key, entry, cleanupError)
		if active && entry.terminalError != nil && registry.report != nil {
			registry.report(entry.terminalError)
		}
	}()
	select {
	case <-ready:
		registry.mu.Lock()
		if ctx.Err() == nil && lifetime.Err() == nil && !entry.stopping && !registry.closed &&
			!entry.exited {
			entry.active = true
			registry.mu.Unlock()
			return false, nil
		}
		registry.mu.Unlock()
	case <-entry.done:
		if entry.terminalError != nil {
			return false, entry.terminalError
		}
		return false, errors.New("controller exited before readiness")
	case <-ctx.Done():
	case <-lifetime.Done():
	}
	entry.cancel()
	<-entry.done
	return false, errors.Join(context.Canceled, ctx.Err(), entry.terminalError)
}

func (registry *controllerRegistry) finish(
	key controllerKey,
	entry *controllerEntry,
	cleanupError error,
) {
	entry.cleanupError = cleanupError
	entry.cancel()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	delete(registry.entries, key)
	delete(registry.endpoints, entry.endpoint)
	close(entry.done)
}

func (registry *controllerRegistry) stop(ctx context.Context, key controllerKey) error {
	registry.mu.Lock()
	if _, reserved := registry.operationKeys[key]; reserved {
		registry.mu.Unlock()
		return errControllerBusy
	}
	entry := registry.entries[key]
	if entry == nil {
		registry.mu.Unlock()
		return nil
	}
	entry.stopping = true
	entry.cancel()
	registry.mu.Unlock()
	select {
	case <-entry.done:
		return entry.cleanupError
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (registry *controllerRegistry) acquire(endpoint controllerEndpoint) (func(), error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed || registry.ctx.Err() != nil {
		return nil, context.Canceled
	}
	if _, exists := registry.operationEndpoints[endpoint]; exists {
		return nil, errControllerBusy
	}
	if _, exists := registry.endpoints[endpoint]; exists {
		return nil, errControllerBusy
	}
	registry.endpoints[endpoint] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(
			func() { registry.mu.Lock(); delete(registry.endpoints, endpoint); registry.mu.Unlock() },
		)
	}, nil
}

func (registry *controllerRegistry) close() error {
	registry.mu.Lock()
	registry.closed = true
	registry.cancel()
	entries := make([]*controllerEntry, 0, len(registry.entries))
	for _, entry := range registry.entries {
		entry.stopping = true
		entries = append(entries, entry)
	}
	registry.mu.Unlock()
	var cleanupError error
	for _, entry := range entries {
		<-entry.done
		cleanupError = errors.Join(cleanupError, entry.cleanupError)
	}
	return cleanupError
}

// quiesce retains an operation reservation after the resident releases its lease.
func (registry *controllerRegistry) quiesce(
	ctx context.Context,
	key controllerKey,
	endpoint controllerEndpoint,
) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	registry.mu.Lock()
	if registry.closed || registry.ctx.Err() != nil {
		registry.mu.Unlock()
		return nil, context.Canceled
	}
	if _, exists := registry.operationKeys[key]; exists {
		registry.mu.Unlock()
		return nil, errControllerBusy
	}
	if _, exists := registry.operationEndpoints[endpoint]; exists {
		registry.mu.Unlock()
		return nil, errControllerBusy
	}
	entry := registry.entries[key]
	if entry != nil && entry.endpoint != endpoint {
		registry.mu.Unlock()
		return nil, errControllerBusy
	}
	if _, exists := registry.endpoints[endpoint]; exists && entry == nil {
		registry.mu.Unlock()
		return nil, errControllerBusy
	}
	release := registry.reserveOperationLocked(key, endpoint)
	if entry != nil {
		entry.stopping = true
		entry.cancel()
	}
	registry.mu.Unlock()
	var cleanupError error
	if entry != nil {
		<-entry.done
		cleanupError = entry.cleanupError
	}
	if err := errors.Join(ctx.Err(), registry.ctx.Err(), cleanupError); err != nil {
		release()
		if entry != nil {
			return nil, &controllerStoppedError{cause: err}
		}
		return nil, err
	}
	return release, nil
}

func (registry *controllerRegistry) reserveOperationLocked(
	key controllerKey,
	endpoint controllerEndpoint,
) func() {
	registry.operationKeys[key] = struct{}{}
	registry.operationEndpoints[endpoint] = struct{}{}
	var once sync.Once
	return func() {
		once.Do(func() {
			registry.mu.Lock()
			defer registry.mu.Unlock()
			delete(registry.operationKeys, key)
			delete(registry.operationEndpoints, endpoint)
		})
	}
}

func (registry *controllerRegistry) reserveAdoption(
	key controllerKey,
	endpoint controllerEndpoint,
) (func(), error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed || registry.ctx.Err() != nil {
		return nil, context.Canceled
	}
	if _, exists := registry.operationKeys[key]; exists {
		return nil, errControllerBusy
	}
	if _, exists := registry.operationEndpoints[endpoint]; exists {
		return nil, errControllerBusy
	}
	entry := registry.entries[key]
	if entry != nil &&
		(key.kind != taskController || !entry.active || entry.stopping || entry.endpoint != endpoint) {
		return nil, errControllerBusy
	}
	if _, exists := registry.endpoints[endpoint]; exists && entry == nil {
		return nil, errControllerBusy
	}
	return registry.reserveOperationLocked(key, endpoint), nil
}
