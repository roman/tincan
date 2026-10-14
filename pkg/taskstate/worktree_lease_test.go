package taskstate

import (
	"errors"
	"testing"
)

func TestPooledLaunchCannotRequestPaneBeforeLease(t *testing.T) {
	input := launchForTest()
	input.Worktree = ""
	reserved, err := taskForTransition(t).ReserveInitialLaunch(input)
	if err != nil {
		t.Fatalf("reserve pooled launch: %v", err)
	}
	preparing, err := reserved.BeginLaunchPreparation(input.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preparing.RequestLaunchPane(input.Generation); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("pane request before lease: %v", err)
	}
}

func TestLaunchLeaseFreezesIdentityAndPreservesExactRetry(t *testing.T) {
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
	if _, err := task.RequestLaunchLease(input.Generation, "other"); !errors.Is(
		err,
		ErrInvalidTransition,
	) {
		t.Fatalf("changed acquisition holder: %v", err)
	}
	if _, err := task.ReserveInitialLaunch(input); err != nil {
		t.Fatalf("retry pending acquisition: %v", err)
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
	task, err = task.RecordLaunchLease(input.Generation, lease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.ReserveInitialLaunch(input); err != nil {
		t.Fatalf("retry captured acquisition: %v", err)
	}
	for _, phase := range []LaunchPhase{LaunchReserved, LaunchPreparing} {
		snapshot := task.Snapshot()
		snapshot.LaunchIntent.Phase = phase
		if _, err := NewMigrated(snapshot); err == nil {
			t.Fatalf("accepted a captured lease in phase %s", phase)
		}
	}

	if _, err := task.RecordLaunchLease(input.Generation, lease); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*WorktreeLease){
		func(l *WorktreeLease) { l.ID = "other" },
		func(l *WorktreeLease) { l.Path = "/different" },
		func(l *WorktreeLease) { l.Holder = "other" },
	} {
		changed := lease
		change(&changed)
		if _, err := task.RecordLaunchLease(input.Generation, changed); !errors.Is(
			err,
			ErrInvalidTransition,
		) {
			t.Fatalf("replaced captured lease: %v", err)
		}
	}
	if _, err := task.RequestLaunchPane(input.Generation); err != nil {
		t.Fatalf("pane after lease: %v", err)
	}
	abandoned, err := task.AbandonInitialLaunch(input.Generation)
	if err != nil {
		t.Fatal(err)
	}
	receipt, found := abandoned.LaunchAbandonment(input.Generation)
	if !found || receipt.Lease != lease || receipt.LeaseHolder != lease.Holder ||
		receipt.Project != lease.Project {
		t.Fatalf("abandonment lost lease: %+v", receipt)
	}
}

func TestAbandonedLeaseRejectsMixedRawAndPooledIdentity(t *testing.T) {
	for _, change := range []func(*LaunchAbandonment){
		func(r *LaunchAbandonment) { r.Project = "/project" },
		func(r *LaunchAbandonment) { r.LeaseHolder = "holder" },
		func(r *LaunchAbandonment) { r.Project = "relative"; r.LeaseHolder = "holder" },
	} {
		snapshot := taskForTransition(t).Snapshot()
		receipt := LaunchAbandonment{
			Generation: "gen-1",
			Worktree:   "/worktree",
			Branch:     "task/work",
		}
		change(&receipt)
		snapshot.AbandonedLaunches = []LaunchAbandonment{receipt}
		if _, err := NewMigrated(snapshot); err == nil {
			t.Fatalf("accepted mixed abandonment identity: %+v", receipt)
		}
	}
}
