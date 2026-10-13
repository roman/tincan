package hostservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

func taskEndpointFixture(t *testing.T) (*Server, controllerKey, controllerEndpoint) {
	t.Helper()
	root := t.TempDir()
	if err := taskstate.Initialize(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("concierge")
	server, err := New(
		Config{
			Root:            root,
			MessageRoot:     filepath.Join(root, "messages"),
			Recipient:       recipient,
			HerdrExecutable: executable,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	server.controllers = newControllerRegistry(context.Background())
	t.Cleanup(func() { server.controllers.close() })
	task, err := taskstate.NewMigrated(
		taskstate.Snapshot{
			Definition: *intakeRequest().Intake,
			State:      taskstate.InFlight,
			Worker: &taskstate.Worker{
				Name:       "worker",
				Harness:    "codex",
				Session:    "work",
				Pane:       "pane-1",
				Workspace:  "workspace-1",
				Tab:        "tab-1",
				Project:    "/repo",
				Worktree:   "/worktree",
				Generation: "gen-1",
				AgentArgs:  []string{"resume", "thread-1"},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := task.Encode()
	if err := os.WriteFile(filepath.Join(root, "records", "task-1.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return server, controllerKey{
			kind: taskController,
			id:   "task-1",
		}, controllerEndpoint{
			session: "work",
			pane:    "pane-1",
		}
}

func startEndpointResident(
	t *testing.T,
	server *Server,
	key controllerKey,
	endpoint controllerEndpoint,
	cleanup func() error,
) {
	t.Helper()
	_, err := server.controllers.start(
		context.Background(),
		key,
		endpoint,
		"binding",
		func(context.Context) (preparedController, error) {
			return preparedController{
				Run:   func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
				Close: cleanup,
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestTaskStopJoinsResidentBeforeBackendFailure(t *testing.T) {
	server, key, endpoint := taskEndpointFixture(t)
	cleaned := make(chan struct{})
	startEndpointResident(t, server, key, endpoint, func() error { close(cleaned); return nil })
	_, err := server.perform(
		context.Background(),
		Request{
			Operation:  Stop,
			Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"},
		},
	)
	if err == nil {
		t.Fatal("expected Herdr failure")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("Stop ran without joining resident")
	}
	if !strings.Contains(err.Error(), "supervision is stopped") {
		t.Fatalf("missing stopped diagnostic: %v", err)
	}
	release, err := server.controllers.acquire(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestInvalidCleanupLeavesResidentActive(t *testing.T) {
	server, key, endpoint := taskEndpointFixture(t)
	startEndpointResident(t, server, key, endpoint, nil)
	_, err := server.perform(
		context.Background(),
		Request{
			Operation:  Cleanup,
			Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"},
		},
	)
	if !errors.Is(err, taskstate.ErrInvalidTransition) {
		t.Fatalf("cleanup eligibility: %v", err)
	}
	if _, err := server.controllers.acquire(endpoint); !errors.Is(err, errControllerBusy) {
		t.Fatalf("invalid cleanup stopped resident: %v", err)
	}
}

func TestHistoricalStopLeavesNewerResidentActive(t *testing.T) {
	server, key, endpoint := taskEndpointFixture(t)
	_, err := server.store.Update(
		context.Background(),
		"task-1",
		func(task taskstate.Task) (taskstate.Task, error) {
			snapshot := task.Snapshot()
			snapshot.Receipt = &taskstate.Receipt{Generation: "old-gen", Mode: taskstate.Retain}
			return taskstate.NewMigrated(snapshot)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	startEndpointResident(t, server, key, endpoint, nil)
	if _, err := server.perform(context.Background(), Request{Operation: Stop, Transition: &TransitionRequest{TaskID: "task-1", Generation: "old-gen"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.controllers.acquire(endpoint); !errors.Is(err, errControllerBusy) {
		t.Fatalf("historical retry stopped resident: %v", err)
	}
}

func TestTaskStopRejectsBindingChangedDuringResidentCleanup(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*taskstate.Worker)
	}{
		{"generation", func(worker *taskstate.Worker) { worker.Generation = "gen-new" }},
		{"session", func(worker *taskstate.Worker) { worker.Session = "other" }},
		{"pane", func(worker *taskstate.Worker) { worker.Pane = "pane-new" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, key, endpoint := taskEndpointFixture(t)
			calls := filepath.Join(t.TempDir(), "calls")
			script := "#!/bin/sh\nprintf called > '" + calls + "'\nexit 72\n"
			if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			startEndpointResident(t, server, key, endpoint, func() error {
				// Store transitions forbid changing endpoint identity.
				task, err := server.store.Read(context.Background(), "task-1")
				if err != nil {
					return err
				}
				snapshot := task.Snapshot()
				test.change(snapshot.Worker)
				changed, err := taskstate.NewMigrated(snapshot)
				if err != nil {
					return err
				}
				data, err := changed.Encode()
				if err != nil {
					return err
				}
				return os.WriteFile(
					filepath.Join(server.config.Root, "records", "task-1.json"),
					data,
					0600,
				)
			})
			_, err := server.perform(
				context.Background(),
				Request{
					Operation:  Stop,
					Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"},
				},
			)
			if !errors.Is(err, taskstate.ErrInvalidTransition) {
				t.Fatalf("changed binding: %v", err)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatalf("changed binding reached Herdr: %v", err)
			}
		})
	}
}

func TestCleanupJoinsResidentBeforePaneProbe(t *testing.T) {
	server, key, endpoint := taskEndpointFixture(t)
	if _, err := server.service.Finish(context.Background(), "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
		t.Fatal(err)
	}
	cleaned := make(chan struct{})
	startEndpointResident(t, server, key, endpoint, func() error { close(cleaned); return nil })
	_, err := server.perform(
		context.Background(),
		Request{
			Operation:  Cleanup,
			Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"},
		},
	)
	if err == nil {
		t.Fatal("expected pane probe failure")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("cleanup ran without joining resident")
	}
	if !strings.Contains(err.Error(), "supervision is stopped") {
		t.Fatalf("missing stopped diagnostic: %v", err)
	}
	release, err := server.controllers.acquire(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestTaskStopCleanupFailureReportsStoppedSupervision(t *testing.T) {
	server, key, endpoint := taskEndpointFixture(t)
	expected := errors.New("lease cleanup failed")
	startEndpointResident(t, server, key, endpoint, func() error { return expected })
	_, err := server.perform(
		context.Background(),
		Request{
			Operation:  Stop,
			Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"},
		},
	)
	if !errors.Is(err, expected) || !strings.Contains(err.Error(), "supervision is stopped") {
		t.Fatalf("cleanup state: %v", err)
	}
	release, err := server.controllers.acquire(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
