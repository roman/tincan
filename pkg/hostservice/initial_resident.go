package hostservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/roman/tincan/pkg/taskstate"
)

var errInitialResidentStopped = errors.New("initial resident registration is no longer pending")

func (registry *controllerRegistry) awaitInitialResident(key controllerKey) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed || registry.ctx.Err() != nil {
		return context.Canceled
	}
	if entry := registry.entries[key]; entry != nil && entry.active {
		return nil
	}
	registry.awaitingInitialResident[key] = struct{}{}
	return nil
}

func (registry *controllerRegistry) initialResidentTasks() []controllerKey {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	keys := make([]controllerKey, 0, len(registry.awaitingInitialResident))
	for key := range registry.awaitingInitialResident {
		keys = append(keys, key)
	}
	return keys
}

func (registry *controllerRegistry) forgetInitialResident(key controllerKey) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	delete(registry.awaitingInitialResident, key)
}

func initialResidentEligible(snapshot taskstate.Snapshot) bool {
	return snapshot.State == taskstate.InFlight && snapshot.Worker != nil &&
		!snapshot.Worker.PendingAdoption &&
		snapshot.Completion == nil
}

func (server *Server) reconcileInitialTasks(ctx context.Context) {
	for _, key := range server.controllers.initialResidentTasks() {
		if ctx.Err() != nil {
			return
		}
		attempt, cancel := context.WithTimeout(ctx, operationTimeout)
		err := server.reconcileInitialTask(attempt, key)
		cancel()
		if err != nil && !errors.Is(err, errControllerBusy) &&
			!errors.Is(err, errInitialResidentStopped) &&
			ctx.Err() == nil {
			server.report(fmt.Errorf("restore task %s: %w", key.id, err))
		}
	}
}

func (server *Server) reconcileInitialTask(ctx context.Context, key controllerKey) error {
	release, err := server.controllers.reserveRecoveredLaunch(key)
	if err != nil {
		return err
	}
	defer release()
	task, err := server.store.Read(ctx, key.id)
	if errors.Is(err, taskstate.ErrTaskNotFound) {
		server.controllers.forgetInitialResident(key)
		return nil
	}
	if err != nil {
		return err
	}
	snapshot := task.Snapshot()
	if intent := snapshot.LaunchIntent; intent != nil {
		originalWorktree := intent.Worktree
		if intent.LeaseHolder != "" {
			originalWorktree = ""
		}
		_, err := server.executeInitialLaunch(ctx, LaunchRequest{
			TaskID: key.id, Harness: intent.Harness, Project: intent.Project, Worktree: originalWorktree,
			Branch: intent.Branch, BaseCommit: intent.BaseCommit, Workspace: intent.Workspace,
			AgentArgs: intent.AgentArgs, Body: intent.Body,
		}, recoveryLaunch)
		if err != nil {
			return err
		}
	} else if !initialResidentEligible(snapshot) {
		server.controllers.forgetInitialResident(key)
		return nil
	}
	release()
	_, err = server.supervisionWithMode(
		ctx,
		Supervise,
		SupervisionRequest{TaskID: key.id},
		initialResidentStart,
	)
	return err
}

func (server *Server) runInitialReconciliation(ctx context.Context) {
	ticker := time.NewTicker(server.config.Supervision.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			server.reconcileInitialTasks(ctx)
		}
	}
}

func (registry *controllerRegistry) startInitialResident(
	ctx context.Context,
	key controllerKey,
	endpoint controllerEndpoint,
	specification string,
	prepare func(context.Context) (preparedController, error),
) (bool, error) {
	return registry.startWithMode(ctx, key, endpoint, specification, prepare, initialResidentStart)
}
