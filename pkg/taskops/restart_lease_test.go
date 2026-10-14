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

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/worktree"
)

func TestRestartRequiresRecordedLeaseBeforeAdvancingGeneration(t *testing.T) {
	for _, pending := range []string{"", "gen-2"} {
		t.Run("pending="+pending, func(t *testing.T) {
			service, runner, messages := runningWorkerService(t)
			ctx := context.Background()
			task, err := service.store.Update(
				ctx,
				"task-1",
				func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					worker := snapshot.Worker
					worker.PendingGeneration = pending
					worker.Lease = taskstate.WorktreeLease{
						ID:         "lease-1",
						Holder:     "task-holder",
						Project:    worker.Project,
						Path:       worker.Worktree,
						Branch:     "task/work",
						BaseCommit: strings.Repeat("a", 40),
						State:      taskstate.LeaseHeld,
					}
					return taskstate.NewMigrated(snapshot)
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			runner.absent = true
			worker, err := service.SupervisedWorker(
				"task-1",
				messages,
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.Restart(ctx); err == nil {
				t.Fatal("restart accepted an unverified lease")
			}
			after, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(task.Snapshot(), after.Snapshot()) || runner.starts != 0 {
				t.Fatalf(
					"unverified lease changed worker: %+v; starts=%d",
					after.Snapshot().Worker,
					runner.starts,
				)
			}
		})
	}
}

func TestRestartChecksLeaseAfterServiceReconstruction(t *testing.T) {
	for _, condition := range []string{"matching", "different ID", "different holder", "absent", "rebound during recovery"} {
		t.Run(condition, func(t *testing.T) {
			service, runner, messages := runningWorkerService(t)
			ctx := context.Background()
			task, err := service.store.Update(
				ctx,
				"task-1",
				func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					worker := snapshot.Worker
					worker.Lease = taskstate.WorktreeLease{
						ID:         "lease-1",
						Holder:     "task-holder",
						Project:    worker.Project,
						Path:       worker.Worktree,
						Branch:     "task/work",
						BaseCommit: strings.Repeat("a", 40),
						State:      taskstate.LeaseHeld,
					}
					return taskstate.NewMigrated(snapshot)
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			lease := task.Snapshot().Worker.Lease
			id, holder := lease.ID, lease.Holder
			if condition == "different ID" {
				id = "lease-2"
			}
			if condition == "different holder" {
				holder = "foreign"
			}
			inventory := fmt.Sprintf(
				`[{"path":%q,"lease_id":%q,"lease_holder":%q,"flavor":"git","processes":[]}]`,
				lease.Path,
				id,
				holder,
			)
			if condition == "absent" {
				inventory = "[]"
			}
			t.Setenv("RESTART_LEASE_INVENTORY", inventory)
			executable := filepath.Join(t.TempDir(), "treehouse")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\n[ \"$1\" = status ] && [ \"$2\" = --json ] && [ \"$#\" = 2 ] || exit 72\nprintf '%s' \"$RESTART_LEASE_INVENTORY\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			reconstructed, err := NewWithWorkerTools(
				service.root,
				service.primaryRecipient,
				WorkerTools{
					Commands: service.workerCommands,
					Pool:     worktree.PoolClient{Executable: executable},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if condition == "rebound during recovery" {
				runner.onAgentRead = func() {
					t.Setenv(
						"RESTART_LEASE_INVENTORY",
						fmt.Sprintf(
							`[{"path":%q,"lease_id":"lease-2","lease_holder":"foreign","flavor":"git","processes":[]}]`,
							lease.Path,
						),
					)
				}
			}
			runner.absent = true
			worker, err := reconstructed.SupervisedWorker(
				"task-1",
				messages,
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			err = worker.Restart(ctx)
			after, readErr := service.store.Read(ctx, "task-1")
			if readErr != nil {
				t.Fatal(readErr)
			}
			if condition == "matching" {
				if err != nil || runner.starts != 1 ||
					after.Snapshot().Worker.Generation == "gen-1" ||
					after.Snapshot().Worker.Lease != lease {
					t.Fatalf(
						"valid restart: %+v; %v; starts=%d",
						after.Snapshot().Worker,
						err,
						runner.starts,
					)
				}
				return
			}
			if condition == "rebound during recovery" {
				if !errors.Is(err, worktree.ErrIdentity) || runner.starts != 0 ||
					after.Snapshot().Worker.Lease != lease ||
					after.Snapshot().Worker.PendingLaunch == "" {
					t.Fatalf(
						"rebound during restart: %+v; %v; starts=%d",
						after.Snapshot().Worker,
						err,
						runner.starts,
					)
				}
				return
			}
			if !errors.Is(err, worktree.ErrIdentity) ||
				!reflect.DeepEqual(task.Snapshot(), after.Snapshot()) ||
				runner.starts != 0 {
				t.Fatalf(
					"invalid lease changed worker: %+v; %v; starts=%d",
					after.Snapshot().Worker,
					err,
					runner.starts,
				)
			}
		})
	}
}
