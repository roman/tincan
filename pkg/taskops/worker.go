package taskops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/supervise"
	"github.com/roman/tincan/pkg/taskstate"
)

// SupervisedWorker revalidates a task's endpoint under its task lock.
type SupervisedWorker struct {
	now       func() time.Time
	service   *Service
	taskID    string
	messages  *inbox.Store
	recipient inbox.Recipient
	client    herdr.Client
}

type taskBootstrap struct {
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Task            string `json:"task"`
	SpawnGeneration string `json:"spawn_generation"`
}

// SupervisedWorker prepares supervision without contacting Herdr.
func (service *Service) SupervisedWorker(
	taskID, messageRoot string,
	client herdr.Client,
) (*SupervisedWorker, error) {
	if service == nil || service.store == nil {
		return nil, taskstate.ErrInvalidTask
	}
	if _, err := firstmate.ParseTaskID(taskID); err != nil {
		return nil, err
	}
	recipient, err := inbox.ParseRecipient(taskID)
	if err != nil {
		return nil, err
	}
	messages, err := inbox.New(messageRoot)
	if err != nil {
		return nil, err
	}
	return &SupervisedWorker{
		now:       time.Now,
		service:   service,
		taskID:    taskID,
		messages:  messages,
		recipient: recipient,
		client:    client,
	}, nil
}

// Observe checks one current binding and repairs interrupted launch confirmation.
func (worker *SupervisedWorker) Observe(ctx context.Context) error {
	_, err := worker.service.store.WithTask(
		ctx,
		worker.taskID,
		func(task *taskstate.LockedTask) error {
			bound, err := recoverWorker(task)
			if err != nil {
				return err
			}
			client, err := worker.boundClient(ctx, bound)
			if err != nil {
				return err
			}
			agent, err := client.Get(ctx, bound.Name)
			if errors.Is(err, herdr.ErrNotFound) {
				return supervise.ErrMissing
			}
			if err != nil {
				return err
			}
			if agent.PaneID != bound.Pane || agent.Kind != bound.Harness {
				return fmt.Errorf("unexpected agent for task %s", worker.taskID)
			}
			target, err := herdr.ParseTarget(bound.Session, bound.Pane)
			if err != nil {
				return err
			}
			switch worker.client.Health(ctx, target).Endpoint {
			case herdr.Alive:
				if bound.PendingLaunch != "" {
					if bound.PendingLaunch == bound.Generation {
						if err := worker.finishRestartRecovery(bound); err != nil {
							return err
						}
					}
					transition := taskstate.Task.CompleteLaunch
					if bound.PendingLaunch != bound.Generation {
						transition = taskstate.Task.CancelLaunch
					}
					if _, err := task.Apply(transition); err != nil {
						return err
					}
				}
				return worker.discardStaleBootstraps(bound)
			case herdr.Dead, herdr.Missing:
				return supervise.ErrMissing
			default:
				return fmt.Errorf("Herdr endpoint for task %s is unreadable", worker.taskID)
			}
		},
	)
	return err
}

func recoverWorker(task *taskstate.LockedTask) (taskstate.Worker, error) {
	snapshot := task.Current().Snapshot()
	bound := snapshot.Worker
	if snapshot.State != taskstate.InFlight || bound == nil || bound.PendingAdoption ||
		snapshot.Completion != nil {
		return taskstate.Worker{}, taskstate.ErrInvalidTransition
	}
	if bound.PendingGeneration != "" {
		updated, err := task.Apply(func(current taskstate.Task) (taskstate.Task, error) {
			return current.AdvanceGeneration(bound.PendingGeneration)
		})
		if err != nil {
			return taskstate.Worker{}, err
		}
		bound = updated.Snapshot().Worker
	}
	return *bound, nil
}

func (worker *SupervisedWorker) boundClient(
	ctx context.Context,
	bound taskstate.Worker,
) (herdr.Client, error) {
	client, err := worker.client.ForSession(bound.Session)
	if err != nil {
		return herdr.Client{}, err
	}
	directory, err := client.ForegroundDirectory(ctx, bound.Pane)
	if err != nil {
		return herdr.Client{}, err
	}
	if err := verifyWorktree(bound.Worktree, directory); err != nil {
		return herdr.Client{}, err
	}
	return client, nil
}

func (worker *SupervisedWorker) discardStaleBootstraps(bound taskstate.Worker) error {
	messages, err := worker.messages.List(worker.recipient)
	if err != nil {
		return err
	}
	for _, message := range messages {
		var bootstrap taskBootstrap
		if json.Unmarshal([]byte(message.Body), &bootstrap) != nil ||
			bootstrap.Kind != "task-bootstrap" || bootstrap.Task != worker.taskID ||
			bootstrap.State != filepath.Join(worker.service.root, "tasks") {
			continue
		}
		if bootstrap.SpawnGeneration == bound.Generation {
			continue
		}
		if err := worker.messages.Acknowledge(worker.recipient, message.ID); err != nil {
			return err
		}
	}
	return nil
}
