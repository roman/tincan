package taskops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
	"github.com/roman/tincan/pkg/supervise"
	"github.com/roman/tincan/pkg/taskstate"
)

// SupervisedWorker revalidates a task's endpoint under its task lock.
type SupervisedWorker struct {
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
					transition := taskstate.Task.CompleteLaunch
					if bound.PendingLaunch != bound.Generation {
						transition = taskstate.Task.CancelLaunch
					}
					if _, err := task.Apply(transition); err != nil {
						return err
					}
				}
				return worker.reconcileBootstrap(ctx, client, bound)
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
	recorded, err := os.Stat(bound.Worktree)
	if err != nil {
		return herdr.Client{}, err
	}
	live, err := os.Stat(directory)
	if err != nil {
		return herdr.Client{}, err
	}
	if !recorded.IsDir() || !live.IsDir() || !os.SameFile(recorded, live) {
		return herdr.Client{}, fmt.Errorf("pane %s is outside task worktree", bound.Pane)
	}
	return client, nil
}

func (worker *SupervisedWorker) reconcileBootstrap(
	ctx context.Context,
	client herdr.Client,
	bound taskstate.Worker,
) error {
	messages, err := worker.messages.List(worker.recipient)
	if err != nil {
		return err
	}
	pending := false
	for _, message := range messages {
		var bootstrap taskBootstrap
		if json.Unmarshal([]byte(message.Body), &bootstrap) != nil ||
			bootstrap.Kind != "task-bootstrap" || bootstrap.Task != worker.taskID ||
			bootstrap.State != filepath.Join(worker.service.root, "tasks") {
			continue
		}
		if bootstrap.SpawnGeneration == bound.Generation {
			pending = true
			continue
		}
		if err := worker.messages.Acknowledge(worker.recipient, message.ID); err != nil {
			return err
		}
	}
	if pending {
		return client.Prompt(ctx, bound.Pane, notify.DoorbellText)
	}
	return nil
}
