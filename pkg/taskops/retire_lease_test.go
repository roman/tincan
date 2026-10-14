package taskops

import (
	"context"
	"errors"
	"github.com/roman/tincan/pkg/worktree"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestRetireReleasesOnlyClosedResultLease(t *testing.T) {
	for _, mode := range []taskstate.CompletionMode{taskstate.Close, taskstate.Retain} {
		t.Run(string(mode), func(t *testing.T) {
			service, _, config, _ := pooledLaunchFixture(t)
			ctx := context.Background()
			launched, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
			if err != nil {
				t.Fatal(err)
			}
			bound := launched.Snapshot().Worker
			service, err = NewWithWorkerTools(
				service.root,
				service.primaryRecipient,
				WorkerTools{Commands: service.workerCommands, Pool: config.Pool, Git: config.Git},
			)
			if err != nil {
				t.Fatal(err)
			}
			closedOn := "2026-10-13"
			if mode == taskstate.Retain {
				closedOn = ""
				if _, err := service.PlaceHold(ctx, "task-1", taskstate.Hold{Reason: "Review result"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.Finish(ctx, "task-1", "gen-1", mode, closedOn); err != nil {
				t.Fatal(err)
			}
			runner := &retirementRunner{
				pane:     bound.Pane,
				response: `{"error":{"code":"pane_not_found"}}`,
			}
			retired, err := service.Retire(
				ctx,
				"task-1",
				"gen-1",
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			want := taskstate.LeaseHeld
			if mode == taskstate.Close {
				want = taskstate.LeaseReturned
			}
			receipt := retired.Snapshot().Receipt
			if receipt == nil || receipt.Lease.State != want || receipt.Lease.ID != bound.Lease.ID {
				t.Fatalf("retired lease: %+v", receipt)
			}
			_, found, err := config.Pool.Find(ctx, bound.Project, bound.Lease.Holder)
			if err != nil || found != (mode == taskstate.Retain) {
				t.Fatalf("lease held=%t; %v", found, err)
			}
			branch, err := exec.Command(config.Git.Executable, "-C", bound.Project, "rev-parse", "refs/heads/"+bound.Lease.Branch).
				Output()
			if err != nil || strings.TrimSpace(string(branch)) != bound.Lease.BaseCommit {
				t.Fatalf("result branch lost: %q; %v", branch, err)
			}
			if _, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{}); err != nil {
				t.Fatalf("receipt retry: %v", err)
			}
		})
	}
}

func TestRetireRejectsDetachedAcceptedResult(t *testing.T) {
	service, _, config, _ := pooledLaunchFixture(t)
	ctx := context.Background()
	launched, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
	if err != nil {
		t.Fatal(err)
	}
	bound := launched.Snapshot().Worker
	service, err = NewWithWorkerTools(
		service.root,
		service.primaryRecipient,
		WorkerTools{Commands: service.workerCommands, Pool: config.Pool, Git: config.Git},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-13"); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(config.Git.Executable, "-C", bound.Worktree, "switch", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("detach: %s; %v", output, err)
	}
	runner := &retirementRunner{pane: bound.Pane, response: `{"error":{"code":"pane_not_found"}}`}
	if _, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{SessionRunner: runner}); err == nil {
		t.Fatal("returned a detached result checkout")
	}
	_, found, err := config.Pool.Find(ctx, bound.Project, bound.Lease.Holder)
	if err != nil || !found {
		t.Fatalf("detached result lease lost: held=%t; %v", found, err)
	}
	task, err := service.store.Read(ctx, "task-1")
	if err != nil || task.Snapshot().Completion == nil || task.Snapshot().Worker == nil {
		t.Fatalf("failed cleanup retired the worker: %v", err)
	}
}

func TestRetirePreservesHiddenAndIgnoredWork(t *testing.T) {
	for _, condition := range []string{"assume unchanged", "skip worktree", "ignored file"} {
		t.Run(condition, func(t *testing.T) {
			service, _, config, _ := pooledLaunchFixture(t)
			ctx := context.Background()
			launched, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
			if err != nil {
				t.Fatal(err)
			}
			bound := launched.Snapshot().Worker
			run := func(args ...string) {
				t.Helper()
				output, err := exec.Command(config.Git.Executable, append([]string{"-C", bound.Worktree}, args...)...).
					CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s; %v", args, output, err)
				}
			}
			tracked := filepath.Join(bound.Worktree, "result.txt")
			if err := os.WriteFile(tracked, []byte("committed result\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bound.Worktree, ".gitignore"), []byte("private.txt\n"), 0600); err != nil {
				t.Fatal(err)
			}
			run("add", "result.txt", ".gitignore")
			run(
				"-c",
				"user.name=Test",
				"-c",
				"user.email=test@example.invalid",
				"commit",
				"-m",
				"Result",
			)
			protected := tracked
			switch condition {
			case "assume unchanged":
				run("update-index", "--assume-unchanged", "result.txt")
			case "skip worktree":
				run("update-index", "--skip-worktree", "result.txt")
			case "ignored file":
				protected = filepath.Join(bound.Worktree, "private.txt")
			}
			if err := os.WriteFile(protected, []byte("valuable pending work\n"), 0600); err != nil {
				t.Fatal(err)
			}
			service, err = NewWithWorkerTools(
				service.root,
				service.primaryRecipient,
				WorkerTools{Commands: service.workerCommands, Pool: config.Pool, Git: config.Git},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-13"); err != nil {
				t.Fatal(err)
			}
			runner := &retirementRunner{
				pane:     bound.Pane,
				response: `{"error":{"code":"pane_not_found"}}`,
			}
			if _, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{SessionRunner: runner}); err == nil {
				t.Fatal("returned a checkout with hidden work")
			}
			data, err := os.ReadFile(protected)
			if err != nil || string(data) != "valuable pending work\n" {
				t.Fatalf("hidden work changed: %q; %v", data, err)
			}
			_, found, err := config.Pool.Find(ctx, bound.Project, bound.Lease.Holder)
			if err != nil || !found {
				t.Fatalf("hidden work lease lost: held=%t; %v", found, err)
			}
		})
	}
}

func TestReleaseRetainedLeaseRecoversInterruptedStateWrite(t *testing.T) {
	for _, condition := range []string{"unleased retry", "rebound retry"} {
		t.Run(condition, func(t *testing.T) {
			service, _, config, _ := pooledLaunchFixture(t)
			ctx := context.Background()
			launched, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
			if err != nil {
				t.Fatal(err)
			}
			bound := launched.Snapshot().Worker
			service, err = NewWithWorkerTools(
				service.root,
				service.primaryRecipient,
				WorkerTools{Commands: service.workerCommands, Pool: config.Pool, Git: config.Git},
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.PlaceHold(ctx, "task-1", taskstate.Hold{Reason: "Review result"}); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Retain, ""); err != nil {
				t.Fatal(err)
			}
			runner := &retirementRunner{
				pane:     bound.Pane,
				response: `{"error":{"code":"pane_not_found"}}`,
			}
			retired, err := service.Retire(
				ctx,
				"task-1",
				"gen-1",
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			released, err := service.returnRetiredLease(ctx, retired, "gen-1")
			if err != nil || released.Snapshot().Receipt.Lease.State != taskstate.LeaseReturned {
				t.Fatalf("first return: %+v; %v", released.Snapshot().Receipt, err)
			}
			// Discard the returned task to model a crash before the state write.
			stored, err := service.store.Read(ctx, "task-1")
			if err != nil || stored.Snapshot().Receipt.Lease.State != taskstate.LeaseHeld {
				t.Fatalf("fixture persisted the return: %v", err)
			}
			if condition == "rebound retry" {
				if _, err := config.Pool.Acquire(ctx, bound.Project, "foreign-holder"); err != nil {
					t.Fatal(err)
				}
			}
			retried, err := service.ReleaseWorktree(ctx, "task-1", "gen-1")
			if condition == "rebound retry" {
				if !errors.Is(err, worktree.ErrIdentity) {
					t.Fatalf("rebound return: %v", err)
				}
				after, err := service.store.Read(ctx, "task-1")
				if err != nil || !reflect.DeepEqual(stored.Snapshot(), after.Snapshot()) {
					t.Fatalf("rebound return changed receipt: %v", err)
				}
				return
			}
			if err != nil || retried.Snapshot().Receipt.Lease.State != taskstate.LeaseReturned {
				t.Fatalf("unleased retry: %+v; %v", retried.Snapshot().Receipt, err)
			}
			service.pool.Executable = "/missing/treehouse"
			if _, err := service.ReleaseWorktree(ctx, "task-1", "gen-1"); err != nil {
				t.Fatalf("returned receipt retry contacted pool: %v", err)
			}
		})
	}
}

func TestReleaseWorktreeRejectsUnacceptedWorker(t *testing.T) {
	service, _, config, _ := pooledLaunchFixture(t)
	ctx := context.Background()
	launched, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
	if err != nil {
		t.Fatal(err)
	}
	bound := launched.Snapshot().Worker
	for _, generation := range []string{"gen-1", "unknown-generation"} {
		if _, err := service.ReleaseWorktree(ctx, "task-1", generation); !errors.Is(
			err,
			taskstate.ErrInvalidTransition,
		) {
			t.Fatalf("released unaccepted or unknown generation: %v", err)
		}
	}
	_, found, err := config.Pool.Find(ctx, bound.Project, bound.Lease.Holder)
	if err != nil || !found {
		t.Fatalf("unaccepted lease lost: held=%t; %v", found, err)
	}
}

func TestReleaseRecoversAbandonedAcquisition(t *testing.T) {
	for _, schema := range []string{"uncaptured", "previous captured schema"} {
		t.Run(schema, func(t *testing.T) {
			service, base, config, _ := pooledLaunchFixture(t)
			ctx := context.Background()
			if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.BeginLaunchPreparation("gen-1") }); err != nil {
				t.Fatal(err)
			}
			if _, err := service.store.RequestLaunchLease(ctx, "task-1", "gen-1", "lost-holder"); err != nil {
				t.Fatal(err)
			}
			published, err := config.Pool.Acquire(ctx, base.intent.Project, "lost-holder")
			if err != nil {
				t.Fatal(err)
			}
			if schema == "previous captured schema" {
				captured := taskstate.WorktreeLease{
					ID:         published.ID,
					Holder:     published.Holder,
					Project:    base.intent.Project,
					Path:       published.Path,
					Branch:     base.intent.Branch,
					BaseCommit: base.intent.BaseCommit,
					State:      taskstate.LeaseHeld,
				}
				if _, err := service.store.RecordLaunchLease(ctx, "task-1", "gen-1", captured); err != nil {
					t.Fatal(err)
				}
			}
			abandoned, err := service.store.Update(
				ctx,
				"task-1",
				func(task taskstate.Task) (taskstate.Task, error) { return task.AbandonInitialLaunch("gen-1") },
			)
			if err != nil {
				t.Fatal(err)
			}
			receipt, _ := abandoned.LaunchAbandonment("gen-1")
			if schema == "uncaptured" &&
				(receipt.Lease.ID != "" || receipt.LeaseHolder != "lost-holder") {
				t.Fatalf("lost-response abandonment: %+v", receipt)
			}
			if schema == "previous captured schema" {
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.AbandonedLaunches[0].BaseCommit = ""
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			service, err = NewWithWorkerTools(
				service.root,
				service.primaryRecipient,
				WorkerTools{Commands: service.workerCommands, Pool: config.Pool, Git: config.Git},
			)
			if err != nil {
				t.Fatal(err)
			}
			released, err := service.ReleaseWorktree(ctx, "task-1", "gen-1")
			if err != nil {
				t.Fatalf("abandoned acquisition release: %v", err)
			}
			result, _ := released.LaunchAbandonment("gen-1")
			if result.Lease.ID != published.ID || result.Lease.Path != published.Path ||
				result.Lease.State != taskstate.LeaseReturned {
				t.Fatalf("released wrong acquisition: %+v", result)
			}
			_, found, err := config.Pool.Find(ctx, base.intent.Project, "lost-holder")
			if err != nil || found {
				t.Fatalf("abandoned acquisition still held=%t: %v", found, err)
			}
		})
	}
}
