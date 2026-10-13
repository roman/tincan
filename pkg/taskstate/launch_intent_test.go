package taskstate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func launchForTest() LaunchIntent {
	return LaunchIntent{
		Generation: "gen-1", Harness: "codex", Name: "worker-1", Session: "work",
		Project: "/project", Worktree: "/worktree", Branch: "task/work",
		BaseCommit: strings.Repeat("a", 40), Workspace: "workspace-1",
		Label: "concierge-gen-1", AgentArgs: []string{"--model", "example"},
		Body: "Do the requested work.\n",
	}
}

func TestInitialLaunchSurvivesStorageAndExactRetry(t *testing.T) {
	store, err := NewStore(versionTwoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), taskForTransition(t)); err != nil {
		t.Fatal(err)
	}
	request := launchForTest()
	reserved, err := store.ReserveInitialLaunch(context.Background(), "task-1", request)
	if err != nil {
		t.Fatal(err)
	}
	request.AgentArgs[0] = "changed"
	loaded, err := store.Read(context.Background(), "task-1")
	if err != nil || !reflect.DeepEqual(loaded.Snapshot(), reserved.Snapshot()) {
		t.Fatalf("stored launch = %#v, %v", loaded.Snapshot(), err)
	}
	copy := loaded.Snapshot()
	copy.LaunchIntent.AgentArgs[0] = "changed"
	if loaded.Snapshot().LaunchIntent.AgentArgs[0] != "--model" {
		t.Fatal("snapshot aliases launch arguments")
	}
	if _, err := store.ReserveInitialLaunch(context.Background(), "task-1", launchForTest()); err != nil {
		t.Fatal(err)
	}
	conflict := launchForTest()
	conflict.Body = "Different work"
	if _, err := store.ReserveInitialLaunch(context.Background(), "task-1", conflict); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("conflicting retry = %v", err)
	}
}

func TestInitialLaunchOrderingAndCancellation(t *testing.T) {
	reserved, err := taskForTransition(t).ReserveInitialLaunch(launchForTest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserved.CancelInitialLaunch("gen-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := reserved.RequestLaunchPane("gen-1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skipped preparation: %v", err)
	}
	preparing, err := reserved.BeginLaunchPreparation("gen-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparing.BeginLaunchPreparation("gen-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := preparing.CancelInitialLaunch("gen-1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cancelled effects: %v", err)
	}
	requested, err := preparing.RequestLaunchPane("gen-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requested.RequestLaunchAgent("gen-1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("started without endpoint: %v", err)
	}
	endpoint := LaunchEndpoint{Workspace: "workspace-1", Tab: "tab-1", Pane: "pane-1"}
	bound, err := requested.RecordLaunchEndpoint("gen-1", endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.RecordLaunchEndpoint("gen-1", endpoint); err != nil {
		t.Fatal(err)
	}
	other := endpoint
	other.Pane = "pane-2"
	if _, err := bound.RecordLaunchEndpoint("gen-1", other); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("replaced endpoint: %v", err)
	}
	held, err := bound.PlaceHold(Hold{Reason: "Wait"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.RequestLaunchAgent("gen-1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("launched held task: %v", err)
	}
	started, err := bound.RequestLaunchAgent("gen-1")
	if err != nil {
		t.Fatal(err)
	}
	resume := []string{"resume", "actual-thread"}
	started, err = started.RecordLaunchResume("gen-1", resume)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := started.RecordLaunchResume("gen-1", resume); err != nil {
		t.Fatal(err)
	}
	if _, err := started.RecordLaunchResume("gen-1", []string{"resume", "other"}); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("replaced native identity: %v", err)
	}
	worker := workerForTransition()
	worker.AgentArgs = resume
	adopted, err := started.ReserveAdoption(worker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adopted.CancelAdoption(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cancelled launch adoption: %v", err)
	}
	adopted, err = adopted.ConfirmAdoption(endpoint.Workspace, endpoint.Tab)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adopted.CompleteInitialLaunch("gen-1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cleared before dispatch: %v", err)
	}
	running, err := adopted.Start()
	if err != nil || running.Snapshot().LaunchIntent == nil {
		t.Fatalf("dispatch lost intent: %v", err)
	}
	if _, err := running.AcceptResult("gen-1", Close, "2026-10-12"); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("finished pending launch: %v", err)
	}
	if _, err := running.ReserveLaunch("gen-2"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("restarted pending launch: %v", err)
	}
	complete, err := running.CompleteInitialLaunch("gen-1")
	if err != nil || complete.Snapshot().LaunchIntent != nil {
		t.Fatalf("complete = %#v, %v", complete.Snapshot(), err)
	}
	if _, err := complete.CompleteInitialLaunch("gen-1"); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchReservationProtectsResourcesAcrossTasks(t *testing.T) {
	for _, resource := range []string{"name", "worktree"} {
		t.Run(resource, func(t *testing.T) {
			store, _ := NewStore(versionTwoRoot(t))
			first := taskForTransition(t)
			second, _ := NewQueued(
				Definition{ID: "task-2", Title: "Other", Repo: "exmachina", Kind: "ship"},
			)
			for _, task := range []Task{first, second} {
				if err := store.Create(context.Background(), task); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.ReserveInitialLaunch(context.Background(), first.ID(), launchForTest()); err != nil {
				t.Fatal(err)
			}
			other := launchForTest()
			other.Generation, other.Label, other.Branch = "gen-2", "concierge-gen-2", "task/other"
			if resource == "name" {
				other.Worktree = "/other"
			} else {
				other.Name = "worker-2"
			}
			if _, err := store.ReserveInitialLaunch(context.Background(), second.ID(), other); !errors.Is(
				err,
				ErrWorkerConflict,
			) {
				t.Fatalf("resource reused: %v", err)
			}
			worker := workerForTransition()
			worker.Worktree = "/other"
			if _, err := store.ReserveAdoption(context.Background(), second.ID(), worker); !errors.Is(
				err,
				ErrWorkerConflict,
			) {
				t.Fatalf("adoption reused reserved name: %v", err)
			}
		})
	}
}

func TestInitialLaunchRejectsInvalidRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*LaunchIntent)
	}{
		{"unknown phase", func(i *LaunchIntent) { i.Phase = "unknown" }},
		{"relative project", func(i *LaunchIntent) { i.Project = "project" }},
		{"relative worktree", func(i *LaunchIntent) { i.Worktree = "worktree" }},
		{"invalid base", func(i *LaunchIntent) { i.BaseCommit = "HEAD" }},
		{"invalid harness", func(i *LaunchIntent) { i.Harness = "unknown" }},
		{"invalid generation", func(i *LaunchIntent) { i.Generation = "" }},
		{"early endpoint", func(i *LaunchIntent) {
			i.Endpoint = &LaunchEndpoint{Workspace: i.Workspace, Tab: "tab-1", Pane: "pane-1"}
		}},
		{"early resume", func(i *LaunchIntent) { i.ResumeArgs = []string{"resume", "thread"} }},
		{"agent without endpoint", func(i *LaunchIntent) { i.Phase = LaunchAgentRequested }},
		{"invalid body", func(i *LaunchIntent) { i.Body = "bad\x00body" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := launchForTest()
			intent.Phase = LaunchReserved
			test.change(&intent)
			snapshot := taskForTransition(t).Snapshot()
			snapshot.LaunchIntent = &intent
			if _, err := NewMigrated(snapshot); !errors.Is(err, ErrInvalidTask) {
				t.Fatalf("accepted malformed intent: %v", err)
			}
		})
	}
}

func TestInitialLaunchRequiresGlobalResourceReservation(t *testing.T) {
	store, _ := NewStore(versionTwoRoot(t))
	queued := taskForTransition(t)
	reserved, err := queued.ReserveInitialLaunch(launchForTest())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), reserved); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Create bypassed resource reservation: %v", err)
	}
	if err := store.Create(context.Background(), queued); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(context.Background(), queued.ID(), func(task Task) (Task, error) {
		return task.ReserveInitialLaunch(launchForTest())
	}); !errors.Is(err, ErrWorkerConflict) {
		t.Fatalf("Update bypassed reservation: %v", err)
	}
}

func TestInitialLaunchRejectsStaleExecutor(t *testing.T) {
	old, err := taskForTransition(t).ReserveInitialLaunch(launchForTest())
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := old.CancelInitialLaunch("gen-1")
	if err != nil {
		t.Fatal(err)
	}
	next := launchForTest()
	next.Generation = "gen-2"
	replacement, err := cleared.ReserveInitialLaunch(next)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := LaunchEndpoint{Workspace: "workspace-1", Tab: "tab-1", Pane: "pane-1"}
	for _, test := range []struct {
		name     string
		phase    LaunchPhase
		endpoint *LaunchEndpoint
		apply    func(Task) (Task, error)
	}{
		{"prepare", LaunchReserved, nil, func(task Task) (Task, error) { return task.BeginLaunchPreparation("gen-1") }},
		{"request pane", LaunchPreparing, nil, func(task Task) (Task, error) { return task.RequestLaunchPane("gen-1") }},
		{"record endpoint", LaunchPaneRequested, nil, func(task Task) (Task, error) { return task.RecordLaunchEndpoint("gen-1", endpoint) }},
		{"request agent", LaunchPaneRequested, &endpoint, func(task Task) (Task, error) { return task.RequestLaunchAgent("gen-1") }},
		{"record resume", LaunchAgentRequested, &endpoint, func(task Task) (Task, error) {
			return task.RecordLaunchResume("gen-1", []string{"resume", "old-thread"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := replacement.Snapshot()
			snapshot.LaunchIntent.Phase = test.phase
			snapshot.LaunchIntent.Endpoint = test.endpoint
			current, err := NewMigrated(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := test.apply(current); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("stale executor advanced replacement: %v", err)
			}
		})
	}
}

func TestInitialLaunchEndpointRejectsAnotherTasksPane(t *testing.T) {
	store, err := NewStore(versionTwoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		task, err := NewQueued(Definition{ID: id, Title: id, Repo: "exmachina", Kind: "ship"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Create(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		intent := launchForTest()
		intent.Name, intent.Worktree, intent.Branch = id, "/"+id, "task/"+id
		intent.Generation, intent.Label = "gen-"+id, "concierge-"+id
		if _, err := store.ReserveInitialLaunch(context.Background(), id, intent); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Update(context.Background(), id, func(current Task) (Task, error) {
			preparing, err := current.BeginLaunchPreparation(intent.Generation)
			if err != nil {
				return Task{}, err
			}
			return preparing.RequestLaunchPane(intent.Generation)
		}); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := LaunchEndpoint{Workspace: "workspace-1", Tab: "tab-1", Pane: "pane-1"}
	if _, err := store.RecordLaunchEndpoint(context.Background(), "task-1", "gen-task-1", endpoint); err != nil {
		t.Fatal(err)
	}
	before, err := store.Read(context.Background(), "task-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordLaunchEndpoint(context.Background(), "task-2", "gen-task-2", endpoint); !errors.Is(
		err,
		ErrWorkerConflict,
	) {
		t.Fatalf("pane assigned twice: %v", err)
	}
	after, err := store.Read(context.Background(), "task-2")
	if err != nil || !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
		t.Fatalf("conflict changed intent: %v", err)
	}
}
