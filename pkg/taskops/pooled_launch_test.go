package taskops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/worktree"
)

func pooledLaunchFixture(
	t *testing.T,
) (*Service, *initialLaunchRunner, InitialLaunchConfig, *ownedLaunchRunner) {
	t.Helper()
	executable := os.Getenv("TREEHOUSE_TEST_EXECUTABLE")
	if executable == "" {
		t.Skip("set TREEHOUSE_TEST_EXECUTABLE to the pinned treehouse binary")
	}
	service, base, config := initialLaunchFixture(t)
	ctx := context.Background()
	if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.CancelInitialLaunch("gen-1") }); err != nil {
		t.Fatal(err)
	}
	base.intent.Worktree = ""
	base.intent.Workspace = ""
	if err := os.WriteFile(filepath.Join(base.intent.Project, "treehouse.toml"), []byte("max_trees = 2\nroot = \"./\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.ReserveInitialLaunch(ctx, "task-1", base.intent); err != nil {
		t.Fatal(err)
	}
	config.Pool = worktree.PoolClient{Executable: executable}
	runner := &ownedLaunchRunner{initialLaunchRunner: base}
	config.Herdr.SessionRunner = runner
	return service, base, config, runner
}

func TestPooledLaunchCapturesLeaseAndCreatesExactBaseBranch(t *testing.T) {
	service, base, config, _ := pooledLaunchFixture(t)
	ctx := context.Background()
	result, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
	if err != nil {
		t.Fatalf("pooled launch: %v", err)
	}
	worker := result.Snapshot().Worker
	if worker == nil || worker.Lease.ID == "" || worker.Lease.State != taskstate.LeaseHeld ||
		worker.Lease.Path != worker.Worktree ||
		worker.Lease.Project != worker.Project ||
		worker.Lease.BaseCommit != base.intent.BaseCommit ||
		worker.OwnedWorkspaceLabel == "" {
		t.Fatalf("pooled worker: %+v", worker)
	}
	if err := config.Git.VerifyIdentity(ctx, worktree.Specification{Project: worker.Project, Path: worker.Worktree, Branch: worker.Lease.Branch, BaseCommit: worker.Lease.BaseCommit}); err != nil {
		t.Fatal(err)
	}
}

func TestPooledLaunchRejectsReboundLeaseOnRetry(t *testing.T) {
	for _, checkpoint := range []string{"workspace", "agent"} {
		t.Run(checkpoint, func(t *testing.T) {
			service, base, config, runner := pooledLaunchFixture(t)
			ctx := context.Background()
			if checkpoint == "workspace" {
				runner.loseResponse = true
			} else {
				base.failure = "agent response"
			}
			if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); err == nil {
				t.Fatal("lost response succeeded")
			}
			task, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			lease := task.Snapshot().LaunchIntent.Lease
			if err := config.Pool.Return(ctx, lease.Project, worktree.Lease{ID: lease.ID, Path: lease.Path, Holder: lease.Holder}); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Pool.Acquire(ctx, lease.Project, "foreign-holder"); err != nil {
				t.Fatal(err)
			}
			starts := base.starts
			runner.loseResponse = false
			base.failure = ""
			if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); !errors.Is(
				err,
				ErrInitialLaunchIdentity,
			) {
				t.Fatalf("rebound lease retry: %v", err)
			}
			if base.starts != starts || runner.workspaceCreates != 1 {
				t.Fatalf(
					"effects repeated: starts=%d workspaces=%d",
					base.starts,
					runner.workspaceCreates,
				)
			}
		})
	}
}

func TestPooledLaunchRecoversLostAcquisitionWithoutAnotherGet(t *testing.T) {
	service, _, config, _ := pooledLaunchFixture(t)
	ctx := context.Background()
	probe := t.TempDir()
	wrapper := filepath.Join(probe, "treehouse")
	t.Setenv("LOST_LEASE_REAL", config.Pool.Executable)
	t.Setenv("LOST_LEASE_PROBE", probe)
	script := `#!/bin/sh
case "$1" in
get)
 printf 'get\n' >> "$LOST_LEASE_PROBE/gets"
 "$LOST_LEASE_REAL" "$@" > "$LOST_LEASE_PROBE/acquired" || exit "$?"
 exit 72
 ;;
*) exec "$LOST_LEASE_REAL" "$@" ;;
esac
`
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config.Pool.Executable = wrapper
	if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); err == nil {
		t.Fatal("lost acquisition response succeeded")
	}
	task, err := service.store.Read(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	intent := task.Snapshot().LaunchIntent
	if intent.Phase != taskstate.LaunchLeaseRequested || intent.LeaseHolder == "" ||
		intent.Worktree != "" {
		t.Fatalf("lost acquisition: %+v", intent)
	}
	published, found, err := config.Pool.Find(ctx, intent.Project, intent.LeaseHolder)
	if err != nil || !found {
		t.Fatalf("published lease: %+v; %v", published, err)
	}
	result, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
	if err != nil {
		t.Fatal(err)
	}
	lease := result.Snapshot().Worker.Lease
	if lease.ID != published.ID || lease.Path != published.Path ||
		lease.Holder != published.Holder {
		t.Fatalf("recovery replaced lease: %+v; original=%+v", lease, published)
	}
	calls, err := os.ReadFile(filepath.Join(probe, "gets"))
	if err != nil || string(calls) != "get\n" {
		t.Fatalf("acquisition calls: %q; %v", calls, err)
	}
}
