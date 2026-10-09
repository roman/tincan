package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/supervise"
)

type registeredWorker struct {
	registry *crew.Registry
	taskID   string
	client   herdr.Client
}

func workerFromCLI(args cli, client herdr.Client) (supervise.Worker, error) {
	if args.Task != "" {
		if args.Root == "" || args.Pane != "" || args.Name != "" || args.Kind != "" ||
			len(args.AgentArgs) != 0 {
			return nil, errors.New("task mode requires --root and excludes manual launch flags")
		}
		if _, err := firstmate.ParseTaskID(args.Task); err != nil {
			return nil, err
		}
		registry, err := crew.New(args.Root)
		if err != nil {
			return nil, err
		}
		return registeredWorker{registry: registry, taskID: args.Task, client: client}, nil
	}
	if args.Root != "" || args.Pane == "" || args.Name == "" || args.Kind == "" {
		return nil, errors.New("use --root and --task, or supply --pane, --name, and --kind")
	}
	return herdrWorker{
		client: client, pane: args.Pane, name: args.Name, kind: args.Kind, args: args.AgentArgs,
	}, nil
}

func (worker registeredWorker) Observe(ctx context.Context) error {
	return worker.registry.WithCurrentTask(worker.taskID, func(task *crew.CurrentTask) error {
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
			return nil
		case herdr.Dead, herdr.Missing:
			return supervise.ErrMissing
		default:
			return fmt.Errorf("Herdr endpoint for task %s is unreadable", record.TaskID)
		}
	})
}

func (worker registeredWorker) Restart(ctx context.Context) error {
	return worker.registry.WithCurrentTask(worker.taskID, func(task *crew.CurrentTask) error {
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
		generation, err := newSpawnGeneration()
		if err != nil {
			return err
		}
		if err := task.AdvanceGeneration(generation); err != nil {
			return err
		}
		return client.StartWithArgs(ctx, record.PaneID, record.Name, record.Kind, record.AgentArgs)
	})
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
