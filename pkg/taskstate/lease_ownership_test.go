package taskstate

import (
	"context"
	"errors"
	"testing"
)

func TestStoreAllowsIndependentPendingPoolAcquisitions(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(versionTwoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		task, err := NewQueued(Definition{ID: id, Title: "Work", Repo: "repo", Kind: "ship"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
		intent := launchForTest()
		intent.Worktree, intent.Name, intent.Generation, intent.Label, intent.Branch = "", id, id, "concierge-"+id, id
		if _, err := store.ReserveInitialLaunch(ctx, id, intent); err != nil {
			t.Fatalf("reserve %s: %v", id, err)
		}
		if _, err := store.Update(ctx, id, func(task Task) (Task, error) { return task.BeginLaunchPreparation(id) }); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RequestLaunchLease(ctx, id, id, "holder-"+id); err != nil {
			t.Fatalf("request %s: %v", id, err)
		}
	}
}

func TestHeldHistoricalLeaseBlocksNewPathClaims(t *testing.T) {
	for _, owner := range []string{"receipt", "prior receipt", "abandoned launch"} {
		for _, state := range []LeaseState{LeaseHeld, LeaseReturned} {
			t.Run(owner+"/"+string(state), func(t *testing.T) {
				input := launchForTest()
				lease := WorktreeLease{
					ID:         "lease-1",
					Holder:     "holder",
					Project:    input.Project,
					Path:       input.Worktree,
					Branch:     input.Branch,
					BaseCommit: input.BaseCommit,
					State:      state,
				}
				snapshot := taskForTransition(t).Snapshot()
				switch owner {
				case "receipt":
					snapshot.Receipt = &Receipt{Generation: "old", Mode: Retain, Lease: lease}
				case "prior receipt":
					snapshot.Receipt = &Receipt{Generation: "latest", Mode: Retain}
					snapshot.PriorReceipts = []Receipt{
						{Generation: "old", Mode: Retain, Lease: lease},
					}
				case "abandoned launch":
					snapshot.AbandonedLaunches = []LaunchAbandonment{
						{
							Generation:  "old",
							Worktree:    lease.Path,
							Branch:      lease.Branch,
							Project:     lease.Project,
							LeaseHolder: lease.Holder,
							Lease:       lease,
						},
					}
				}
				historical, err := NewMigrated(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"task-1", "task-2"} {
					candidate, err := NewQueued(
						Definition{ID: id, Title: "Next", Repo: "repo", Kind: "ship"},
					)
					if err != nil {
						t.Fatal(err)
					}
					candidate, err = candidate.ReserveInitialLaunch(input)
					if err != nil {
						t.Fatal(err)
					}
					err = checkWorkerIdentity([]Task{historical}, candidate)
					if state == LeaseHeld && !errors.Is(err, ErrWorkerConflict) {
						t.Fatalf("claimed held historical path: %v", err)
					}
					if state == LeaseReturned && err != nil {
						t.Fatalf("returned path remained reserved: %v", err)
					}
				}
			})
		}
	}
}

func TestWorkerLeaseSurvivesRestartAndRetirement(t *testing.T) {
	for _, mode := range []CompletionMode{Close, Retain} {
		t.Run(string(mode), func(t *testing.T) {
			snapshot := runningTaskForRestart(t).Snapshot()
			input := launchForTest()
			lease := WorktreeLease{
				ID:         "lease-1",
				Holder:     "holder",
				Project:    input.Project,
				Path:       input.Worktree,
				Branch:     input.Branch,
				BaseCommit: input.BaseCommit,
				State:      LeaseHeld,
			}
			snapshot.Worker.Lease = lease
			task, err := NewMigrated(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			task, err = task.ReserveLaunch("gen-2")
			if err != nil {
				t.Fatal(err)
			}
			task, err = task.AdvanceGeneration("gen-2")
			if err != nil {
				t.Fatal(err)
			}
			task, err = task.CompleteLaunch()
			if err != nil {
				t.Fatal(err)
			}
			if task.Snapshot().Worker.Lease != lease {
				t.Fatal("restart lost lease")
			}
			closedOn := "2026-10-13"
			if mode == Retain {
				closedOn = ""
				task, err = task.PlaceHold(Hold{Reason: "Review result", Kind: "captain"})
				if err != nil {
					t.Fatal(err)
				}
			}
			task, err = task.AcceptResult("gen-2", mode, closedOn)
			if err != nil {
				t.Fatal(err)
			}
			task, err = task.RetireResult("gen-2")
			if err != nil {
				t.Fatal(err)
			}
			if task.Snapshot().Receipt.Lease != lease {
				t.Fatal("retirement lost lease")
			}
			loaded, err := NewMigrated(task.Snapshot())
			if err != nil || loaded.Snapshot().Receipt.Lease != lease {
				t.Fatalf("reload lost lease: %v", err)
			}
		})
	}
}

func TestAbandonedLeaseCaptureChecksEveryPathOwner(t *testing.T) {
	for _, owner := range []string{"task-1", "task-2"} {
		for _, resource := range []string{"worker", "launch", "current receipt", "prior receipt", "abandoned receipt", "returned receipt"} {
			t.Run(owner+"/"+resource, func(t *testing.T) {
				ctx := context.Background()
				store, err := NewStore(versionTwoRoot(t))
				if err != nil {
					t.Fatal(err)
				}
				input := launchForTest()
				input.Worktree = ""
				task, err := taskForTransition(t).ReserveInitialLaunch(input)
				if err != nil {
					t.Fatal(err)
				}
				task, err = task.BeginLaunchPreparation(input.Generation)
				if err != nil {
					t.Fatal(err)
				}
				task, err = task.RequestLaunchLease(input.Generation, "holder")
				if err != nil {
					t.Fatal(err)
				}
				task, err = task.AbandonInitialLaunch(input.Generation)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Create(ctx, task); err != nil {
					t.Fatal(err)
				}
				if owner != "task-1" {
					other, err := NewQueued(
						Definition{ID: owner, Title: "Other", Repo: "repo", Kind: "ship"},
					)
					if err != nil {
						t.Fatal(err)
					}
					if err := store.Create(ctx, other); err != nil {
						t.Fatal(err)
					}
				}
				lease := WorktreeLease{
					ID:         "lease-1",
					Holder:     "holder",
					Project:    input.Project,
					Path:       "/worktree",
					Branch:     input.Branch,
					BaseCommit: input.BaseCommit,
					State:      LeaseHeld,
				}
				switch resource {
				case "worker":
					worker := workerForTransition()
					worker.Name, worker.Generation = "next-worker", "gen-2"
					if _, err := store.ReserveAdoption(ctx, owner, worker); err != nil {
						t.Fatal(err)
					}
				case "launch":
					next := launchForTest()
					next.Name, next.Generation, next.Label, next.Branch = "next-worker", "gen-2", "concierge-gen-2", "task/next"
					if _, err := store.ReserveInitialLaunch(ctx, owner, next); err != nil {
						t.Fatal(err)
					}
				default:
					if _, err := store.Update(ctx, owner, func(task Task) (Task, error) {
						snapshot := task.Snapshot()
						occupied := lease
						occupied.ID, occupied.Holder = "other-lease", "other-holder"
						receipt := Receipt{Generation: "old", Mode: Retain, Lease: occupied}
						switch resource {
						case "current receipt":
							snapshot.Receipt = &receipt
						case "prior receipt":
							snapshot.Receipt = &Receipt{Generation: "recent", Mode: Retain}
							snapshot.PriorReceipts = []Receipt{receipt}
						case "returned receipt":
							receipt.Lease.State = LeaseReturned
							snapshot.Receipt = &receipt
						case "abandoned receipt":
							snapshot.AbandonedLaunches = append(snapshot.AbandonedLaunches, LaunchAbandonment{Generation: "old", Lease: occupied, LeaseHolder: occupied.Holder, Project: occupied.Project, Worktree: occupied.Path, Branch: occupied.Branch})
						}
						return NewMigrated(snapshot)
					}); err != nil {
						t.Fatal(err)
					}
				}
				_, err = store.RecordAbandonedLease(ctx, "task-1", input.Generation, lease)
				if resource == "returned receipt" {
					if err != nil {
						t.Fatalf("returned path prevented capture: %v", err)
					}
				} else if !errors.Is(err, ErrWorkerConflict) {
					t.Fatalf("captured occupied path: %v", err)
				}
			})
		}
	}
}
