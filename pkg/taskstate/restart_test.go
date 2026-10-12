package taskstate

import (
	"errors"
	"testing"
)

func runningTaskForRestart(t *testing.T) Task {
	t.Helper()
	reserved, err := taskForTransition(t).ReserveAdoption(workerForTransition())
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := reserved.ConfirmAdoption("workspace-1", "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	running, err := adopted.Start()
	if err != nil {
		t.Fatal(err)
	}
	return running
}

func TestRestartGenerationSurvivesEachStep(t *testing.T) {
	running := runningTaskForRestart(t)
	reserved, err := running.ReserveLaunch("gen-2")
	if err != nil {
		t.Fatal(err)
	}
	if worker := reserved.Snapshot().Worker; worker.Generation != "gen-1" ||
		worker.PendingLaunch != "gen-2" {
		t.Fatalf("reserved worker = %#v", worker)
	}
	retried, err := reserved.ReserveLaunch("gen-3")
	if err != nil || retried.Snapshot().Worker.PendingLaunch != "gen-2" {
		t.Fatalf("reservation retry = %#v, %v", retried.Snapshot(), err)
	}
	advanced, err := retried.AdvanceGeneration("gen-2")
	if err != nil {
		t.Fatal(err)
	}
	if worker := advanced.Snapshot().Worker; worker.Generation != "gen-2" ||
		worker.PendingLaunch != "gen-2" {
		t.Fatalf("advanced worker = %#v", worker)
	}
	if _, err := advanced.AdvanceGeneration("gen-2"); err != nil {
		t.Fatalf("generation retry = %v", err)
	}
	completed, err := advanced.CompleteLaunch()
	if err != nil || completed.Snapshot().Worker.PendingLaunch != "" {
		t.Fatalf("completed worker = %#v, %v", completed.Snapshot(), err)
	}
	if _, err := completed.CompleteLaunch(); err != nil {
		t.Fatalf("completion retry = %v", err)
	}
}

func TestRestartCanCancelOnlyFutureLaunch(t *testing.T) {
	running := runningTaskForRestart(t)
	reserved, err := running.ReserveLaunch("gen-2")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := reserved.CancelLaunch()
	if err != nil || cancelled.Snapshot().Worker.PendingLaunch != "" {
		t.Fatalf("cancelled worker = %#v, %v", cancelled.Snapshot(), err)
	}
	if _, err := cancelled.CancelLaunch(); err != nil {
		t.Fatalf("cancellation retry = %v", err)
	}
	if _, err := reserved.CompleteLaunch(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("completed unstarted launch = %v", err)
	}
	advanced, err := reserved.AdvanceGeneration("gen-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanced.CancelLaunch(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("cancelled active generation = %v", err)
	}
}

func TestRestartRejectsInvalidGenerationAndUnboundTask(t *testing.T) {
	for _, test := range []struct {
		name       string
		task       func(*testing.T) Task
		generation string
	}{
		{name: "current generation", task: runningTaskForRestart, generation: "gen-1"},
		{name: "malformed generation", task: runningTaskForRestart, generation: "bad generation"},
		{name: "unbound task", task: taskForTransition, generation: "gen-2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.task(t).ReserveLaunch(test.generation); !errors.Is(
				err, ErrInvalidTransition,
			) {
				t.Fatalf("reserve = %v", err)
			}
		})
	}
}

func TestRestartRecoversMigratedPendingGeneration(t *testing.T) {
	for _, pendingLaunch := range []string{"", "gen-2"} {
		t.Run("pending launch "+pendingLaunch, func(t *testing.T) {
			worker := workerForTransition()
			worker.Workspace = "workspace-1"
			worker.Tab = "tab-1"
			worker.PendingGeneration = "gen-2"
			worker.PendingLaunch = pendingLaunch
			task, err := NewMigrated(Snapshot{
				Definition: taskForTransition(t).Definition(), State: InFlight, Worker: &worker,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := task.AdvanceGeneration("gen-3"); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("different generation = %v", err)
			}
			recovered, err := task.AdvanceGeneration("gen-2")
			if err != nil {
				t.Fatal(err)
			}
			got := recovered.Snapshot().Worker
			expectedLaunch := pendingLaunch
			if expectedLaunch == "" {
				expectedLaunch = "gen-2"
			}
			if got.Generation != "gen-2" || got.PendingGeneration != "" ||
				got.PendingLaunch != expectedLaunch {
				t.Fatalf("recovered worker = %#v", got)
			}
			if _, err := recovered.AcceptResult("gen-2", Close, "2026-10-11"); !errors.Is(
				err, ErrInvalidTransition,
			) {
				t.Fatalf("result before launch confirmation = %v", err)
			}
			confirmed, err := recovered.CompleteLaunch()
			if err != nil || confirmed.Snapshot().Worker.PendingLaunch != "" {
				t.Fatalf("confirmed launch = %#v, %v", confirmed.Snapshot(), err)
			}
		})
	}
}
