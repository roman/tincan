package taskstate

import (
	"errors"
	"testing"
)

func workerForTransition() Worker {
	return Worker{
		Name: "worker-1", Harness: "codex", Session: "work",
		Pane: "pane-1", Project: "/project", Worktree: "/worktree",
		Generation: "gen-1", AgentArgs: []string{"resume", "thread-1"},
	}
}

func taskForTransition(t *testing.T) Task {
	t.Helper()
	task, err := NewQueued(Definition{
		ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship",
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestTaskAdoptionStartAndClose(t *testing.T) {
	task := taskForTransition(t)
	reserved, err := task.ReserveAdoption(workerForTransition())
	if err != nil {
		t.Fatal(err)
	}
	if reserved.State() != Queued || reserved.Snapshot().Worker == nil ||
		!reserved.Snapshot().Worker.PendingAdoption {
		t.Fatalf("reservation = %#v", reserved.Snapshot())
	}
	if _, err := reserved.Start(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("pending adoption started: %v", err)
	}
	adopted, err := reserved.ConfirmAdoption("workspace-1", "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	running, err := adopted.Start()
	if err != nil || running.State() != InFlight {
		t.Fatalf("Start = %#v, %v", running.Snapshot(), err)
	}
	if _, err := running.Start(); err != nil {
		t.Fatalf("idempotent Start failed: %v", err)
	}
	finished, err := running.AcceptResult("gen-1", Close, "2026-10-11")
	if err != nil || finished.State() != Done ||
		finished.Snapshot().Completion == nil ||
		finished.Snapshot().Completion.Phase != Accepted {
		t.Fatalf("AcceptResult = %#v, %v", finished.Snapshot(), err)
	}
	if _, err := finished.AcceptResult("gen-1", Close, "2026-10-11"); err != nil {
		t.Fatalf("idempotent result failed: %v", err)
	}
	retired, err := finished.RetireResult("gen-1")
	if err != nil || retired.Snapshot().Worker != nil ||
		retired.Snapshot().Completion != nil ||
		retired.Snapshot().Receipt == nil ||
		retired.Snapshot().Receipt.Generation != "gen-1" {
		t.Fatalf("RetireResult = %#v, %v", retired.Snapshot(), err)
	}
	if _, err := retired.RetireResult("gen-1"); err != nil {
		t.Fatalf("idempotent retirement failed: %v", err)
	}
	if _, err := retired.AcceptResult("gen-1", Close, "2026-10-11"); err != nil {
		t.Fatalf("result replay after cleanup failed: %v", err)
	}
}

func TestTaskRejectsStaleResultAndHeldClose(t *testing.T) {
	task := taskForTransition(t)
	reserved, err := task.ReserveAdoption(workerForTransition())
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := reserved.ConfirmAdoption("workspace-1", "tab-1")
	if err != nil {
		t.Fatal(err)
	}
	held, err := adopted.PlaceHold(Hold{Reason: "Waiting", Kind: "captain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Start(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("held task started: %v", err)
	}
	released, err := held.ReleaseHold()
	if err != nil {
		t.Fatal(err)
	}
	running, err := released.Start()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := running.AcceptResult("other-gen", Close, "2026-10-11"); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("stale result accepted: %v", err)
	}
	heldRunning, err := running.PlaceHold(Hold{Reason: "Waiting", Kind: "captain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := heldRunning.AcceptResult("gen-1", Close, "2026-10-11"); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("held task closed: %v", err)
	}
	retained, err := heldRunning.AcceptResult("gen-1", Retain, "")
	if err != nil || retained.State() != Queued {
		t.Fatalf("retained result = %#v, %v", retained.Snapshot(), err)
	}
}

func TestTaskRecoversAcceptedResultBeforeQueueTransition(t *testing.T) {
	worker := workerForTransition()
	worker.Workspace, worker.Tab = "workspace-1", "tab-1"
	task, err := NewMigrated(Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      InFlight, Worker: &worker,
		Completion: &Completion{Generation: "gen-1", Mode: Close, Phase: Accepted},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := task.AcceptResult("gen-1", Close, "2026-10-11")
	if err != nil || finished.State() != Done || finished.Definition().Closed != "2026-10-11" {
		t.Fatalf("accepted recovery = %#v, %v", finished.Snapshot(), err)
	}
}

func TestTaskAdoptionRecoveryKeepsPublishedEndpoint(t *testing.T) {
	task := taskForTransition(t)
	reserved, err := task.ReserveAdoption(workerForTransition())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := reserved.Snapshot()
	snapshot.Worker.Workspace, snapshot.Worker.Tab = "workspace-1", "tab-1"
	partlyPublished, err := NewMigrated(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partlyPublished.ConfirmAdoption("workspace-2", "tab-2"); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("adoption replaced a published endpoint: %v", err)
	}
	if _, err := partlyPublished.CancelAdoption(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("adoption cancelled a published endpoint: %v", err)
	}
	confirmed, err := partlyPublished.ConfirmAdoption("workspace-1", "tab-1")
	if err != nil || confirmed.Snapshot().Worker.PendingAdoption {
		t.Fatalf("adoption recovery = %#v, %v", confirmed.Snapshot(), err)
	}
}

func TestTaskRecoversRetiredResultBeforeQueueTransition(t *testing.T) {
	for _, mode := range []CompletionMode{Close, Retain} {
		t.Run(string(mode), func(t *testing.T) {
			definition := Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"}
			closedOn := "2026-10-11"
			expectedState := Done
			if mode == Retain {
				definition.Hold = &Hold{Reason: "Waiting", Kind: "captain"}
				closedOn = ""
				expectedState = Queued
			}
			task, err := NewMigrated(Snapshot{
				Definition: definition, State: InFlight,
				Completion: &Completion{Generation: "gen-1", Mode: mode, Phase: Retired},
			})
			if err != nil {
				t.Fatal(err)
			}
			updated, err := task.AcceptResult("gen-1", mode, closedOn)
			if err != nil || updated.State() != expectedState ||
				updated.Snapshot().Completion.Phase != Retired {
				t.Fatalf("retired recovery = %#v, %v", updated.Snapshot(), err)
			}
		})
	}
}

func TestTaskRejectsCancellationAndMalformedReservation(t *testing.T) {
	done, err := NewMigrated(Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      Done,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := done.CancelAdoption(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("done task cancelled adoption: %v", err)
	}
	malformed := workerForTransition()
	malformed.Generation = ""
	if _, err := taskForTransition(t).ReserveAdoption(malformed); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("malformed reservation error = %v", err)
	}
	retained, err := NewMigrated(Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      Queued, Receipt: &Receipt{Generation: "gen-1", Mode: Retain},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retained.CancelAdoption(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("retained task cancelled adoption: %v", err)
	}
	reserved, err := taskForTransition(t).ReserveAdoption(workerForTransition())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserved.ConfirmAdoption("bad workspace", "tab-1"); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("malformed endpoint error = %v", err)
	}
	if _, err := reserved.PlaceHold(Hold{Reason: "bad\nreason"}); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("malformed hold error = %v", err)
	}
}

func TestTaskRetirementPreservesEarlierReceipts(t *testing.T) {
	task, err := NewMigrated(Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      Queued, Receipt: &Receipt{Generation: "gen-1", Mode: Retain},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := workerForTransition()
	worker.Generation = "gen-2"
	reserved, err := task.ReserveAdoption(worker)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := reserved.ConfirmAdoption("workspace-1", "tab-2")
	if err != nil {
		t.Fatal(err)
	}
	running, err := adopted.Start()
	if err != nil {
		t.Fatal(err)
	}
	held, err := running.PlaceHold(Hold{Reason: "More work", Kind: "captain"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := held.AcceptResult("gen-2", Retain, "")
	if err != nil {
		t.Fatal(err)
	}
	retired, err := accepted.RetireResult("gen-2")
	if err != nil {
		t.Fatal(err)
	}
	if !retired.HasReceipt("gen-1", Retain) || !retired.HasReceipt("gen-2", Retain) ||
		len(retired.Snapshot().PriorReceipts) != 1 {
		t.Fatalf("retirement lost a receipt: %#v", retired.Snapshot())
	}
	if _, err := retired.RetireResult("gen-1"); err != nil {
		t.Fatalf("historical retirement retry failed: %v", err)
	}
	encoded, err := retired.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !decoded.HasReceipt("gen-1", Retain) {
		t.Fatalf("decoded receipt history = %#v, %v", decoded.Snapshot(), err)
	}
}

func TestTaskRetirementKeepsMatchingReceipt(t *testing.T) {
	task, err := NewMigrated(Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship",
			Hold: &Hold{Reason: "More work", Kind: "captain"}},
		State: Queued, Receipt: &Receipt{Generation: "gen-1", Mode: Retain},
		Completion: &Completion{Generation: "gen-1", Mode: Retain, Phase: Retired},
	})
	if err != nil {
		t.Fatal(err)
	}
	retired, err := task.RetireResult("gen-1")
	if err != nil || len(retired.Snapshot().PriorReceipts) != 0 ||
		retired.Snapshot().Receipt.Generation != "gen-1" {
		t.Fatalf("matching retirement = %#v, %v", retired.Snapshot(), err)
	}
}
