package taskops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/taskstate"
)

type adoptionRunner struct {
	workerRunner
	workspace string
	tab       string
	pane      string
	paneReads int
	failFinal bool
}

func (runner *adoptionRunner) RunSession(
	ctx context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	if len(args) >= 2 && args[0] == "pane" && args[1] == "get" {
		runner.paneReads++
		if runner.failFinal && runner.paneReads >= 3 {
			return nil, nil, errors.New("final verification unavailable")
		}
		return []byte(
			fmt.Sprintf(
				`{"result":{"pane":{"pane_id":%q,"foreground_cwd":%q,"workspace_id":%q,"tab_id":%q}}}`,
				runner.pane,
				runner.worktree,
				runner.workspace,
				runner.tab,
			),
		), nil, nil
	}
	return runner.workerRunner.RunSession(ctx, session, args...)
}

func adoptionFixture(t *testing.T) (*Service, AdoptionInput, *adoptionRunner) {
	t.Helper()
	service, err := New(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), taskstate.Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"}); err != nil {
		t.Fatal(err)
	}
	input := AdoptionInput{
		TaskID:     "task-1",
		Generation: "gen-existing",
		Name:       "worker-1",
		Session:    "work",
		Pane:       "pane-1",
		Project:    service.root,
		Worktree:   t.TempDir(),
		AgentArgs:  []string{"resume", "thread-1"},
	}
	return service, input, &adoptionRunner{
		workerRunner: workerRunner{worktree: input.Worktree, harness: "codex"},
		workspace:    "workspace-1",
		tab:          "tab-1",
		pane:         "pane-1",
	}
}

func TestAdoptWorkerVerifiesAndRetriesCurrentBinding(t *testing.T) {
	for _, state := range []string{"new", "pending", "partial pending", "confirmed", "in flight", "symlink worktree"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			service, input, runner := adoptionFixture(t)
			if state == "symlink worktree" {
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(input.Worktree, alias); err != nil {
					t.Fatal(err)
				}
				runner.worktree = alias
			}
			if state != "new" && state != "symlink worktree" {
				worker := taskstate.Worker{
					Name:       input.Name,
					Harness:    "codex",
					Session:    input.Session,
					Pane:       input.Pane,
					Project:    input.Project,
					Worktree:   input.Worktree,
					Generation: "gen-existing",
					AgentArgs:  input.AgentArgs,
				}
				if _, err := service.ReserveAdoption(ctx, input.TaskID, worker); err != nil {
					t.Fatal(err)
				}
				if state == "partial pending" {
					if _, err := service.store.Update(ctx, input.TaskID, func(task taskstate.Task) (taskstate.Task, error) {
						snapshot := task.Snapshot()
						snapshot.Worker.Workspace = runner.workspace
						snapshot.Worker.Tab = runner.tab
						return taskstate.NewMigrated(snapshot)
					}); err != nil {
						t.Fatal(err)
					}
				}
				if state == "confirmed" || state == "in flight" {
					if _, err := service.ConfirmAdoption(ctx, input.TaskID, runner.workspace, runner.tab); err != nil {
						t.Fatal(err)
					}
				}
				if state == "in flight" {
					if _, err := service.Start(ctx, input.TaskID); err != nil {
						t.Fatal(err)
					}
				}
			}
			client := herdr.Client{SessionRunner: runner}
			adopted, err := service.AdoptWorker(ctx, input, client)
			if err != nil {
				t.Fatal(err)
			}
			bound := adopted.Snapshot().Worker
			want := taskstate.Worker{
				Name:       input.Name,
				Harness:    "codex",
				Session:    input.Session,
				Pane:       input.Pane,
				Project:    input.Project,
				Worktree:   input.Worktree,
				Generation: input.Generation,
				AgentArgs:  input.AgentArgs,
				Workspace:  runner.workspace,
				Tab:        runner.tab,
			}
			if !reflect.DeepEqual(*bound, want) || bound.Generation == "" {
				t.Fatalf("worker = %#v, want %#v", bound, want)
			}
			if state != "new" && state != "symlink worktree" && bound.Generation != "gen-existing" {
				t.Fatalf("retry changed generation: %#v", bound)
			}
			retried, err := service.AdoptWorker(ctx, input, client)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(retried.Snapshot(), adopted.Snapshot()) {
				t.Fatalf("retry changed task: %#v", retried.Snapshot())
			}
		})
	}
}

func TestAdoptWorkerInvalidEndpointLeavesNoReservation(t *testing.T) {
	for _, state := range []string{"wrong directory", "missing agent", "empty harness", "wrong pane", "unreadable health"} {
		t.Run(state, func(t *testing.T) {
			service, input, runner := adoptionFixture(t)
			switch state {
			case "wrong directory":
				runner.worktree = t.TempDir()
			case "missing agent":
				runner.absent = true
			case "empty harness":
				runner.harness = ""
			case "wrong pane":
				runner.pane = "pane-2"
			case "unreadable health":
				runner.unreadable = true
			}
			if _, err := service.AdoptWorker(context.Background(), input, herdr.Client{SessionRunner: runner}); err == nil {
				t.Fatal("invalid endpoint accepted")
			}
			current, err := service.store.Read(context.Background(), input.TaskID)
			if err != nil || current.Snapshot().Worker != nil {
				t.Fatalf("invalid endpoint reserved worker: %#v, %v", current.Snapshot(), err)
			}
		})
	}
}

func TestAdoptWorkerRecoversFailedFinalVerification(t *testing.T) {
	service, input, runner := adoptionFixture(t)
	runner.failFinal = true
	ctx := context.Background()
	client := herdr.Client{SessionRunner: runner}
	if _, err := service.AdoptWorker(ctx, input, client); err == nil {
		t.Fatal("failed final verification accepted")
	}
	pending, err := service.store.Read(ctx, input.TaskID)
	if err != nil || pending.Snapshot().Worker == nil ||
		!pending.Snapshot().Worker.PendingAdoption {
		t.Fatalf("reservation = %#v, %v", pending.Snapshot(), err)
	}
	runner.failFinal = false
	adopted, err := service.AdoptWorker(ctx, input, client)
	if err != nil || adopted.Snapshot().Worker.PendingAdoption ||
		adopted.Snapshot().Worker.Generation != pending.Snapshot().Worker.Generation {
		t.Fatalf("recovery = %#v, %v", adopted.Snapshot(), err)
	}
}

func TestAdoptWorkerRejectsChangedBindingAndIneligibleTask(t *testing.T) {
	for _, state := range []string{"generation", "pending generation conflict", "in flight generation conflict", "name", "arguments", "project", "worktree", "session", "pane", "harness", "workspace", "tab", "partial workspace", "completion", "pending generation", "pending launch"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			service, input, runner := adoptionFixture(t)
			worker := taskstate.Worker{
				Name:       input.Name,
				Harness:    "codex",
				Session:    input.Session,
				Pane:       input.Pane,
				Project:    input.Project,
				Worktree:   input.Worktree,
				Generation: "gen-existing",
				AgentArgs:  input.AgentArgs,
			}
			if _, err := service.ReserveAdoption(ctx, input.TaskID, worker); err != nil {
				t.Fatal(err)
			}
			if state == "partial workspace" {
				if _, err := service.store.Update(ctx, input.TaskID, func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.Worker.Workspace = "workspace-old"
					snapshot.Worker.Tab = runner.tab
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			} else if state != "pending generation conflict" {
				if _, err := service.ConfirmAdoption(ctx, input.TaskID, runner.workspace, runner.tab); err != nil {
					t.Fatal(err)
				}
			}
			switch state {
			case "generation", "pending generation conflict", "in flight generation conflict":
				if state == "in flight generation conflict" {
					if _, err := service.Start(ctx, input.TaskID); err != nil {
						t.Fatal(err)
					}
				}
				input.Generation = "gen-other"
			case "name":
				input.Name = "other-worker"
			case "arguments":
				input.AgentArgs = []string{"resume", "other-thread"}
			case "project":
				input.Project = t.TempDir()
			case "worktree":
				input.Worktree = t.TempDir()
			case "session":
				input.Session = "other-session"
			case "pane":
				input.Pane = "pane-2"
			case "harness":
				runner.harness = "claude"
			case "workspace":
				runner.workspace = "workspace-2"
			case "tab":
				runner.tab = "tab-2"
			case "completion", "pending generation", "pending launch":
				if _, err := service.Start(ctx, input.TaskID); err != nil {
					t.Fatal(err)
				}
				if _, err := service.store.Update(ctx, input.TaskID, func(task taskstate.Task) (taskstate.Task, error) {
					if state == "completion" {
						return task.AcceptResult("gen-existing", taskstate.Close, "2026-10-12")
					}
					snapshot := task.Snapshot()
					if state == "pending generation" {
						snapshot.Worker.PendingGeneration = "gen-next"
					} else {
						snapshot.Worker.PendingLaunch = "gen-next"
					}
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := service.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.AdoptWorker(ctx, input, herdr.Client{SessionRunner: runner}); err == nil {
				t.Fatal("changed or ineligible binding accepted")
			}
			after, err := service.store.Read(ctx, input.TaskID)
			if err != nil || !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatalf("rejection changed record: %#v, %v", after.Snapshot(), err)
			}
		})
	}
}

func TestAdoptWorkerStopsAfterReservationFailureOrReplacement(t *testing.T) {
	for _, state := range []string{"uncertain publication", "cancelled", "replaced generation"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			service, input, runner := adoptionFixture(t)
			reserve := func(ctx context.Context, id string, worker taskstate.Worker) (taskstate.Task, error) {
				reserved, err := service.store.ReserveAdoption(ctx, id, worker)
				if err != nil {
					return reserved, err
				}
				if state == "uncertain publication" {
					return reserved, taskstate.ErrPublicationUnconfirmed
				}
				if _, err := service.store.Update(ctx, id, taskstate.Task.CancelAdoption); err != nil {
					t.Fatal(err)
				}
				if state == "replaced generation" {
					worker.Generation = "gen-replacement"
					if _, err := service.store.ReserveAdoption(ctx, id, worker); err != nil {
						t.Fatal(err)
					}
				}
				return reserved, nil
			}
			_, err := service.adoptWorker(ctx, input, herdr.Client{SessionRunner: runner}, reserve)
			if err == nil ||
				(state == "uncertain publication" && !errors.Is(err, taskstate.ErrPublicationUnconfirmed)) {
				t.Fatalf("interrupted adoption = %v", err)
			}
			current, readErr := service.store.Read(ctx, input.TaskID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			worker := current.Snapshot().Worker
			if runner.paneReads != 2 || (state == "cancelled" && worker != nil) ||
				(worker != nil && !worker.PendingAdoption) {
				t.Fatalf(
					"interrupted adoption advanced: %#v, pane reads %d",
					worker,
					runner.paneReads,
				)
			}
			if state == "uncertain publication" {
				retried, err := service.AdoptWorker(ctx, input, herdr.Client{SessionRunner: runner})
				if err != nil || retried.Snapshot().Worker.Generation != worker.Generation ||
					retried.Snapshot().Worker.PendingAdoption {
					t.Fatalf("uncertain retry = %#v, %v", retried.Snapshot(), err)
				}
			}
		})
	}
}

func TestAdoptWorkerRejectsAnotherTasksEndpoint(t *testing.T) {
	for _, state := range []string{"name", "pane"} {
		t.Run(state, func(t *testing.T) {
			service, input, runner := adoptionFixture(t)
			ctx := context.Background()
			if _, err := service.Create(ctx, taskstate.Definition{ID: "task-2", Title: "Other", Repo: "exmachina", Kind: "ship"}); err != nil {
				t.Fatal(err)
			}
			worker := taskstate.Worker{
				Name:       input.Name,
				Harness:    "codex",
				Session:    input.Session,
				Pane:       input.Pane,
				Project:    input.Project,
				Worktree:   input.Worktree,
				Generation: "gen-other",
				AgentArgs:  input.AgentArgs,
			}
			if state == "name" {
				worker.Pane = "pane-other"
			} else {
				worker.Name = "other-worker"
			}
			if _, err := service.ReserveAdoption(ctx, "task-2", worker); err != nil {
				t.Fatal(err)
			}
			if _, err := service.AdoptWorker(ctx, input, herdr.Client{SessionRunner: runner}); !errors.Is(
				err,
				taskstate.ErrWorkerConflict,
			) {
				t.Fatalf("conflicting adoption = %v", err)
			}
		})
	}
}

func TestAdoptWorkerAcceptsConcurrentConfirmationOfSameReservation(t *testing.T) {
	for _, state := range []string{"confirmed", "started", "conflicting location"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			service, input, runner := adoptionFixture(t)
			var generation string
			reserve := func(ctx context.Context, id string, worker taskstate.Worker) (taskstate.Task, error) {
				pending, err := service.store.ReserveAdoption(ctx, id, worker)
				if err != nil {
					return pending, err
				}
				generation = worker.Generation
				workspace := runner.workspace
				if state == "conflicting location" {
					workspace = "workspace-other"
				}
				if _, err := service.ConfirmAdoption(ctx, id, workspace, runner.tab); err != nil {
					t.Fatal(err)
				}
				if state == "started" {
					if _, err := service.Start(ctx, id); err != nil {
						t.Fatal(err)
					}
				}
				return pending, nil
			}
			adopted, err := service.adoptWorker(
				ctx,
				input,
				herdr.Client{SessionRunner: runner},
				reserve,
			)
			if state == "conflicting location" {
				if err == nil {
					t.Fatal("conflicting published location accepted")
				}
				return
			}
			wantState := taskstate.Queued
			if state == "started" {
				wantState = taskstate.InFlight
			}
			if err != nil || adopted.State() != wantState {
				t.Fatalf("concurrent confirmation = %#v, %v", adopted.Snapshot(), err)
			}
			bound := adopted.Snapshot().Worker
			if bound == nil || bound.PendingAdoption || bound.Generation != generation ||
				bound.Workspace != runner.workspace ||
				bound.Tab != runner.tab {
				t.Fatalf("adoption changed concurrent binding: %#v", bound)
			}
		})
	}
}

func TestAdoptWorkerRejectsInvalidGenerationBeforeDependencies(t *testing.T) {
	for _, generation := range []string{"", "bad generation", "../generation", "gen\nnext"} {
		t.Run(fmt.Sprintf("%q", generation), func(t *testing.T) {
			service := &Service{}
			_, err := service.AdoptWorker(
				context.Background(),
				AdoptionInput{Generation: generation},
				herdr.Client{},
			)
			if err == nil || !strings.Contains(err.Error(), "generation") {
				t.Fatalf("invalid generation: %v", err)
			}
		})
	}
}

func TestAdoptWorkerRejectsRetiredGeneration(t *testing.T) {
	service, input, runner := adoptionFixture(t)
	ctx := context.Background()
	before, err := service.store.Update(
		ctx,
		input.TaskID,
		func(task taskstate.Task) (taskstate.Task, error) {
			snapshot := task.Snapshot()
			snapshot.Receipt = &taskstate.Receipt{
				Generation: input.Generation,
				Mode:       taskstate.Retain,
			}
			return taskstate.NewMigrated(snapshot)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.AdoptWorker(ctx, input, herdr.Client{SessionRunner: runner})
	if !errors.Is(err, taskstate.ErrInvalidTransition) {
		t.Fatalf("retired generation: %v", err)
	}
	after, err := service.store.Read(ctx, input.TaskID)
	if err != nil || !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
		t.Fatalf("retired generation changed task: %#v, %v", after.Snapshot(), err)
	}
}
