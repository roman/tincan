package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
	"github.com/roman/tincan/pkg/supervise"
)

type registeredWorker struct {
	registry    *crew.Registry
	taskRoot    string
	messageRoot string
	taskID      string
	client      herdr.Client
}

type bootstrapEnvelope struct {
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Task            string `json:"task"`
	SpawnGeneration string `json:"spawn_generation"`
}

func workerFromCLI(args cli, client herdr.Client) (supervise.Worker, error) {
	if args.Task != "" {
		if args.Root == "" || args.MessageRoot == "" || args.Pane != "" ||
			args.Name != "" || args.Kind != "" ||
			len(args.AgentArgs) != 0 {
			return nil, errors.New(
				"task mode requires --root and --message-root and excludes manual launch flags",
			)
		}
		if _, err := firstmate.ParseTaskID(args.Task); err != nil {
			return nil, err
		}
		registry, err := crew.New(args.Root)
		if err != nil {
			return nil, err
		}
		if _, err := inbox.New(args.MessageRoot); err != nil {
			return nil, err
		}
		return registeredWorker{
			registry: registry, taskRoot: args.Root, messageRoot: args.MessageRoot,
			taskID: args.Task, client: client,
		}, nil
	}
	if args.Root != "" || args.MessageRoot != "" || args.Pane == "" ||
		args.Name == "" || args.Kind == "" {
		return nil, errors.New("use --root and --task, or supply --pane, --name, and --kind")
	}
	return herdrWorker{
		client: client, pane: args.Pane, name: args.Name, kind: args.Kind, args: args.AgentArgs,
	}, nil
}

func (worker registeredWorker) Observe(ctx context.Context) error {
	return worker.registry.WithLaunchTask(worker.taskID, func(task *crew.CurrentTask) error {
		record := task.Record()
		client, err := worker.boundClient(ctx, record)
		if err != nil {
			return err
		}
		agent, err := client.Get(ctx, record.Name)
		if errors.Is(err, herdr.ErrNotFound) {
			return supervise.ErrMissing
		}
		if err != nil {
			return err
		}
		if agent.PaneID != record.PaneID || agent.Kind != record.Kind {
			return fmt.Errorf("unexpected agent for task %s", record.TaskID)
		}
		target, err := herdr.ParseTarget(record.HerdrSession, record.PaneID)
		if err != nil {
			return err
		}
		switch worker.client.Health(ctx, target).Endpoint {
		case herdr.Alive:
			if record.PendingLaunch != "" && record.PendingLaunch != record.SpawnGeneration {
				if err := task.CancelLaunch(); err != nil {
					return err
				}
			}
			if record.PendingLaunch != "" && record.PendingLaunch == record.SpawnGeneration {
				if err := task.CompleteLaunch(); err != nil {
					return err
				}
			}
			if err := worker.clearStaleBootstraps(task.Record().SpawnGeneration); err != nil {
				return err
			}
			return worker.promptPendingBootstrap(
				ctx,
				client,
				record.PaneID,
				task.Record().SpawnGeneration,
			)
		case herdr.Dead, herdr.Missing:
			return supervise.ErrMissing
		default:
			return fmt.Errorf("Herdr endpoint for task %s is unreadable", record.TaskID)
		}
	})
}

func (worker registeredWorker) Restart(ctx context.Context) error {
	return worker.registry.WithLaunchTask(worker.taskID, func(task *crew.CurrentTask) error {
		record := task.Record()
		client, err := worker.boundClient(ctx, record)
		if err != nil {
			return err
		}
		if _, err := client.Get(ctx, record.PaneID); err == nil {
			return herdr.ErrAgentPresent
		} else if !errors.Is(err, herdr.ErrNotFound) {
			return err
		}
		candidate, err := newSpawnGeneration()
		if err != nil {
			return err
		}
		generation, err := task.ReserveLaunch(candidate)
		if err != nil {
			return err
		}
		store, err := inbox.New(worker.messageRoot)
		if err != nil {
			return err
		}
		recipient, err := inbox.ParseRecipient(worker.taskID)
		if err != nil {
			return err
		}
		body, err := json.Marshal(bootstrapEnvelope{
			Kind: "task-bootstrap", State: filepath.Join(worker.taskRoot, "tasks"),
			Task: worker.taskID, SpawnGeneration: generation,
		})
		if err != nil {
			return err
		}
		key := fmt.Sprintf("task-bootstrap:%s:%s:%s", worker.taskRoot, worker.taskID, generation)
		if _, err := store.Send(recipient, key, string(body)); err != nil {
			return err
		}
		if record.SpawnGeneration != generation {
			if err := task.AdvanceGeneration(generation); err != nil {
				return err
			}
		}
		if err := client.StartWithArgs(ctx, record.PaneID, record.Name, record.Kind, record.AgentArgs); err != nil {
			return err
		}
		if err := task.CompleteLaunch(); err != nil {
			return err
		}
		return worker.promptPendingBootstrap(ctx, client, record.PaneID, generation)
	})
}

func (worker registeredWorker) promptPendingBootstrap(
	ctx context.Context, client herdr.Client, paneID, generation string,
) error {
	if worker.messageRoot == "" {
		return nil
	}
	store, err := inbox.New(worker.messageRoot)
	if err != nil {
		return err
	}
	recipient, err := inbox.ParseRecipient(worker.taskID)
	if err != nil {
		return err
	}
	messages, err := store.List(recipient)
	if err != nil {
		return err
	}
	for _, message := range messages {
		var bootstrap bootstrapEnvelope
		if json.Unmarshal([]byte(message.Body), &bootstrap) != nil ||
			!worker.matchesBootstrap(bootstrap, generation) {
			continue
		}
		return client.Prompt(ctx, paneID, notify.DoorbellText)
	}
	return nil
}

func (worker registeredWorker) clearStaleBootstraps(currentGeneration string) error {
	if worker.messageRoot == "" {
		return nil
	}
	store, err := inbox.New(worker.messageRoot)
	if err != nil {
		return err
	}
	recipient, err := inbox.ParseRecipient(worker.taskID)
	if err != nil {
		return err
	}
	messages, err := store.List(recipient)
	if err != nil {
		return err
	}
	for _, message := range messages {
		var bootstrap bootstrapEnvelope
		if json.Unmarshal([]byte(message.Body), &bootstrap) != nil ||
			bootstrap.Kind != "task-bootstrap" ||
			bootstrap.State != filepath.Join(worker.taskRoot, "tasks") ||
			bootstrap.Task != worker.taskID ||
			bootstrap.SpawnGeneration == currentGeneration {
			continue
		}
		if err := store.Acknowledge(recipient, message.ID); err != nil {
			return err
		}
	}
	return nil
}

func (worker registeredWorker) matchesBootstrap(message bootstrapEnvelope, generation string) bool {
	return message.Kind == "task-bootstrap" &&
		message.State == filepath.Join(worker.taskRoot, "tasks") &&
		message.Task == worker.taskID && message.SpawnGeneration == generation
}

func (worker registeredWorker) boundClient(
	ctx context.Context,
	record crew.Record,
) (herdr.Client, error) {
	client, err := worker.client.ForSession(record.HerdrSession)
	if err != nil {
		return herdr.Client{}, err
	}
	directory, err := client.ForegroundDirectory(ctx, record.PaneID)
	if err != nil {
		return herdr.Client{}, err
	}
	recorded, err := os.Stat(record.Worktree)
	if err != nil {
		return herdr.Client{}, err
	}
	live, err := os.Stat(directory)
	if err != nil {
		return herdr.Client{}, err
	}
	if !recorded.IsDir() || !live.IsDir() || !os.SameFile(recorded, live) {
		return herdr.Client{}, fmt.Errorf("pane %s is outside task worktree", record.PaneID)
	}
	return client, nil
}

func newSpawnGeneration() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "s" + hex.EncodeToString(random), nil
}
