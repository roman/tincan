package taskstate

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
)

type LeaseState string

const (
	LeaseHeld     LeaseState = "held"
	LeaseReturned LeaseState = "returned"
)

// WorktreeLease keeps the pool identity and result branch after worker retirement.
type WorktreeLease struct {
	ID         string     `json:"id"`
	Holder     string     `json:"holder"`
	Project    string     `json:"project"`
	Path       string     `json:"path"`
	Branch     string     `json:"branch"`
	BaseCommit string     `json:"base_commit"`
	State      LeaseState `json:"state"`
}

func validWorktreeLease(lease WorktreeLease) bool {
	if lease == (WorktreeLease{}) {
		return true
	}
	if !singleLine(lease.ID) || !singleLine(lease.Holder) || !singleLine(lease.Branch) ||
		strings.HasPrefix(lease.Branch, "-") ||
		lease.Branch == "HEAD" ||
		lease.Path == lease.Project ||
		(lease.State != LeaseHeld && lease.State != LeaseReturned) {
		return false
	}
	for _, path := range []string{lease.Path, lease.Project} {
		if _, err := firstmate.ParseAbsolutePath(path); err != nil {
			return false
		}
	}
	if (len(lease.BaseCommit) != 40 && len(lease.BaseCommit) != 64) ||
		strings.ToLower(lease.BaseCommit) != lease.BaseCommit {
		return false
	}
	_, err := hex.DecodeString(lease.BaseCommit)
	return err == nil
}

// RequestLaunchLease fixes the holder before non-idempotent pool acquisition.
func (task Task) RequestLaunchLease(generation, holder string) (Task, error) {
	intent := task.record.LaunchIntent
	if intent == nil || intent.Generation != generation || task.State() != Queued ||
		task.record.Hold != nil ||
		!singleLine(holder) ||
		intent.Worktree != "" ||
		intent.Lease != (WorktreeLease{}) {
		return Task{}, ErrInvalidTransition
	}
	if intent.Phase == LaunchLeaseRequested {
		if intent.LeaseHolder == holder {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	if intent.Phase != LaunchPreparing {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.LaunchIntent.LeaseHolder = holder
	snapshot.LaunchIntent.Phase = LaunchLeaseRequested
	return newTransition(snapshot)
}

// RecordLaunchLease freezes the path and identity returned by the pool.
func (task Task) RecordLaunchLease(generation string, lease WorktreeLease) (Task, error) {
	intent := task.record.LaunchIntent
	if intent == nil || intent.Generation != generation || task.State() != Queued ||
		task.record.Hold != nil {
		return Task{}, ErrInvalidTransition
	}
	if intent.Lease != (WorktreeLease{}) {
		if intent.Lease == lease {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	if intent.Phase != LaunchLeaseRequested || !validWorktreeLease(lease) ||
		lease.State != LeaseHeld ||
		lease.Holder != intent.LeaseHolder ||
		lease.Project != intent.Project ||
		lease.Branch != intent.Branch ||
		lease.BaseCommit != intent.BaseCommit {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.LaunchIntent.Lease = lease
	snapshot.LaunchIntent.Worktree = lease.Path
	snapshot.LaunchIntent.Phase = LaunchLeaseReady
	return newTransition(snapshot)
}

// RequestLaunchLease serializes acquisition identity with other worker reservations.
func (store *Store) RequestLaunchLease(
	ctx context.Context,
	taskID, generation, holder string,
) (Task, error) {
	return store.reserveWorkerResources(
		ctx,
		taskID,
		func(task Task) (Task, error) { return task.RequestLaunchLease(generation, holder) },
	)
}

// RecordLaunchLease reserves the captured path before another task can bind it.
func (store *Store) RecordLaunchLease(
	ctx context.Context,
	taskID, generation string,
	lease WorktreeLease,
) (Task, error) {
	return store.reserveWorkerResources(
		ctx,
		taskID,
		func(task Task) (Task, error) { return task.RecordLaunchLease(generation, lease) },
	)
}
