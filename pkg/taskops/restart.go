package taskops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/taskstate"
)

// Restart resumes one durable launch reservation after rechecking task eligibility.
func (worker *SupervisedWorker) Restart(ctx context.Context) error {
	_, err := worker.service.store.WithTask(
		ctx,
		worker.taskID,
		func(task *taskstate.LockedTask) error {
			if bound := task.Current().Snapshot().Worker; bound != nil {
				if err := validateNativeLaunch(bound.Harness, bound.AgentArgs); err != nil {
					return err
				}
			}
			bound, err := recoverWorker(task)
			if err != nil {
				return err
			}
			client, err := worker.boundClient(ctx, bound)
			if err != nil {
				return err
			}
			if _, err := client.Get(ctx, bound.Pane); err == nil {
				return herdr.ErrAgentPresent
			} else if !errors.Is(err, herdr.ErrNotFound) {
				return err
			}
			generation := bound.PendingLaunch
			if generation == "" {
				random := make([]byte, 16)
				if _, err := rand.Read(random); err != nil {
					return err
				}
				generation = "s" + hex.EncodeToString(random)
			}
			arguments, err := nativeLaunchArguments(bound.Harness, bound.AgentArgs, generation)
			if err != nil {
				return err
			}
			if _, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) {
				return current.ReserveLaunch(generation)
			}); err != nil {
				return err
			}
			if err := worker.sendBootstrap(generation); err != nil {
				return err
			}
			// An equal generation can follow an uncertain publication. Republish it
			// before launching so this process confirms the durable generation.
			if _, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) {
				return current.AdvanceGeneration(generation)
			}); err != nil {
				return err
			}
			if err := client.StartWithArgs(ctx, bound.Pane, bound.Name, bound.Harness, arguments); err != nil {
				return err
			}
			if _, err := task.Apply(taskstate.Task.CompleteLaunch); err != nil {
				return err
			}
			return worker.reconcileBootstrap(ctx, client, *task.Current().Snapshot().Worker)
		},
	)
	return err
}

func (worker *SupervisedWorker) sendBootstrap(generation string) error {
	body, err := json.Marshal(taskBootstrap{
		Kind: "task-bootstrap", State: filepath.Join(worker.service.root, "tasks"),
		Task: worker.taskID, SpawnGeneration: generation,
	})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("task-bootstrap:%s:%s:%s", worker.service.root, worker.taskID, generation)
	_, err = worker.messages.Send(worker.recipient, key, string(body))
	return err
}
