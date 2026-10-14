package taskops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestWorkerRestartRecoversFailedBootstrapPublication(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	runner.absent = true
	if err := os.WriteFile(messageRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker, err := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := worker.Restart(ctx); err == nil {
		t.Fatal("bootstrap failure was lost")
	}
	reserved, err := service.store.Read(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	generation := reserved.Snapshot().Worker.PendingLaunch
	if generation == "" || generation == "gen-1" || runner.starts != 0 {
		t.Fatalf("reservation = %#v; starts = %d", reserved.Snapshot(), runner.starts)
	}
	if err := os.Remove(messageRoot); err != nil {
		t.Fatal(err)
	}
	runner.onStart = func() error {
		current, err := service.store.Read(ctx, "task-1")
		if err != nil {
			return err
		}
		bound := current.Snapshot().Worker
		if bound.Generation != generation || bound.PendingLaunch != generation {
			t.Errorf("generation before start = %#v", bound)
		}
		messages, err := worker.messages.List(worker.recipient)
		if err != nil {
			return err
		}
		if len(messages) != 1 {
			t.Errorf("bootstrap before start = %#v", messages)
			return nil
		}
		wantKey := fmt.Sprintf("task-bootstrap:%s:task-1:%s", service.root, generation)
		if messages[0].Key != wantKey {
			t.Errorf("bootstrap key = %q; want %q", messages[0].Key, wantKey)
		}
		var bootstrap taskBootstrap
		if err := json.Unmarshal([]byte(messages[0].Body), &bootstrap); err != nil {
			return err
		}
		if bootstrap.SpawnGeneration != generation || bootstrap.Task != "task-1" {
			t.Errorf("bootstrap = %#v", bootstrap)
		}
		return nil
	}
	if err := worker.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := service.store.Read(ctx, "task-1")
	if err != nil || current.Snapshot().Worker.PendingLaunch != "" || runner.starts != 1 ||
		runner.prompts != 0 {
		t.Fatalf(
			"restarted = %#v, %v; starts=%d prompts=%d",
			current.Snapshot(),
			err,
			runner.starts,
			runner.prompts,
		)
	}
	wantArgs := []string{
		"agent",
		"start",
		"worker-1",
		"--kind",
		"codex",
		"--pane",
		"pane-1",
		"--",
		"-c",
		fmt.Sprintf("shell_environment_policy.set.CONCIERGE_SPAWN_GENERATION=%q", generation),
		"resume",
		"thread-1",
	}
	if len(runner.startArgs) != len(wantArgs)+1 ||
		!reflect.DeepEqual(runner.startArgs[:len(wantArgs)], wantArgs) {
		t.Fatalf("launch args = %v", runner.startArgs)
	}
	protocol := runner.startArgs[len(wantArgs)]
	if !strings.Contains(protocol, "You are a Concierge worker") ||
		!strings.Contains(protocol, "CONCIERGE_SPAWN_GENERATION") ||
		strings.Contains(protocol, generation) ||
		strings.ContainsAny(protocol, "\r\n") {
		t.Fatalf("resume protocol = %q", protocol)
	}
}

func TestWorkerRestartRecoversAdvancedAndMigratedGenerations(t *testing.T) {
	for _, phase := range []string{"failed launch", "migrated generation"} {
		t.Run(phase, func(t *testing.T) {
			service, runner, messageRoot := runningWorkerService(t)
			runner.absent = true
			ctx := context.Background()
			if phase == "migrated generation" {
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.Worker.PendingGeneration = "gen-2"
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			worker, err := service.SupervisedWorker(
				"task-1",
				messageRoot,
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			runner.onStart = func() error { return errors.New("launch failed") }
			if err := worker.Restart(ctx); err == nil {
				t.Fatal("launch failure was lost")
			}
			current, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			bound := current.Snapshot().Worker
			if bound.PendingLaunch == "" || bound.PendingLaunch != bound.Generation ||
				bound.PendingGeneration != "" {
				t.Fatalf("interrupted worker = %#v", bound)
			}
			if phase == "migrated generation" && bound.Generation != "gen-2" {
				t.Fatalf("migration generation = %s", bound.Generation)
			}
			before, err := worker.messages.List(worker.recipient)
			if err != nil || len(before) != 1 {
				t.Fatalf("bootstrap = %#v, %v", before, err)
			}
			beforeNotices, err := worker.messages.List(service.primaryRecipient)
			if err != nil || len(beforeNotices) != 1 {
				t.Fatalf("restart notice before retry = %+v, %v", beforeNotices, err)
			}
			firstLaunch := append([]string(nil), runner.startArgs...)
			runner.onStart = nil
			if err := worker.Restart(ctx); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(firstLaunch, runner.startArgs) {
				t.Fatalf("retry changed launch: %v -> %v", firstLaunch, runner.startArgs)
			}
			after, err := worker.messages.List(worker.recipient)
			if err != nil || len(after) != 1 || after[0].ID != before[0].ID ||
				!after[0].CreatedAt.Equal(before[0].CreatedAt) {
				t.Fatalf("retry bootstrap = %#v, %v", after, err)
			}
			afterNotices, err := worker.messages.List(service.primaryRecipient)
			if err != nil || len(afterNotices) != 1 ||
				!reflect.DeepEqual(beforeNotices, afterNotices) {
				t.Fatalf("restart retry changed primary notification: %+v, %v", afterNotices, err)
			}
			var notice struct {
				Kind string `json:"kind"`
				From string `json:"from_generation"`
				To   string `json:"target_generation"`
			}
			if err := json.Unmarshal([]byte(afterNotices[0].Body), &notice); err != nil {
				t.Fatal(err)
			}
			if notice.Kind != "task-restart" || notice.To != bound.Generation {
				t.Fatalf("restart notification = %+v", notice)
			}
			if phase == "migrated generation" {
				if notice.From != "" {
					t.Fatalf("legacy notification invented a source: %+v", notice)
				}
			} else if notice.From != "gen-1" {
				t.Fatalf("restart notification lost its source: %+v", notice)
			}
			current, err = service.store.Read(ctx, "task-1")
			if err != nil || current.Snapshot().Worker.Generation != bound.Generation ||
				current.Snapshot().Worker.PendingLaunch != "" {
				t.Fatalf("recovered worker = %#v, %v", current.Snapshot(), err)
			}
			if phase == "migrated generation" && len(current.Snapshot().Worker.Restarts) != 0 {
				t.Fatal("legacy recovery invented a restart history")
			}

		})
	}
}

func TestWorkerRestartRechecksCompletionAndConcurrentPresence(t *testing.T) {
	t.Run("completion after observation", func(t *testing.T) {
		service, runner, messageRoot := runningWorkerService(t)
		runner.absent = true
		worker, err := service.SupervisedWorker(
			"task-1",
			messageRoot,
			herdr.Client{SessionRunner: runner},
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := worker.Observe(ctx); err == nil {
			t.Fatal("missing worker accepted")
		}
		if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-11"); err != nil {
			t.Fatal(err)
		}
		calls := runner.calls
		if err := worker.Restart(ctx); !errors.Is(err, taskstate.ErrInvalidTransition) {
			t.Fatalf("completed restart = %v", err)
		}
		if runner.calls != calls {
			t.Fatal("completed task contacted Herdr")
		}
	})
	t.Run("concurrent supervisors", func(t *testing.T) {
		service, runner, messageRoot := runningWorkerService(t)
		runner.absent = true
		worker, err := service.SupervisedWorker(
			"task-1",
			messageRoot,
			herdr.Client{SessionRunner: runner},
		)
		if err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 2)
		for range 2 {
			go func() { results <- worker.Restart(context.Background()) }()
		}
		successes := 0
		for range 2 {
			err := <-results
			if err == nil {
				successes++
			} else if !errors.Is(err, herdr.ErrAgentPresent) {
				t.Fatal(err)
			}
		}
		if successes != 1 || runner.starts != 1 {
			t.Fatalf("successes=%d starts=%d", successes, runner.starts)
		}
	})
}

func TestWorkerObservationRecoversUnconfirmedLiveLaunch(t *testing.T) {
	service, runner, messageRoot := runningWorkerService(t)
	runner.absent = true
	runner.verificationError = true
	worker, err := service.SupervisedWorker(
		"task-1",
		messageRoot,
		herdr.Client{SessionRunner: runner},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := worker.Restart(ctx); err == nil {
		t.Fatal("verification failure was lost")
	}
	interrupted, err := service.store.Read(ctx, "task-1")
	if err != nil || interrupted.Snapshot().Worker.PendingLaunch == "" || runner.prompts != 0 {
		t.Fatalf(
			"interrupted launch = %#v, %v; prompts = %d",
			interrupted.Snapshot(),
			err,
			runner.prompts,
		)
	}
	runner.verificationError = false
	if err := worker.Observe(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := service.store.Read(ctx, "task-1")
	if err != nil || current.Snapshot().Worker.PendingLaunch != "" || runner.starts != 1 ||
		runner.prompts != 0 {
		t.Fatalf(
			"recovered launch = %#v, %v; starts=%d prompts=%d",
			current.Snapshot(),
			err,
			runner.starts,
			runner.prompts,
		)
	}
}
