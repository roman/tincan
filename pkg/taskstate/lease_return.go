package taskstate

import "context"

// RetiredLease returns the lease for a departed worker or abandoned launch.
func (task Task) RetiredLease(generation string) (WorktreeLease, bool) {
	if receipt := task.record.Receipt; receipt != nil && receipt.Generation == generation {
		return receipt.Lease, true
	}
	for _, receipt := range task.record.PriorReceipts {
		if receipt.Generation == generation {
			return receipt.Lease, true
		}
	}
	for _, receipt := range task.record.AbandonedLaunches {
		if receipt.Generation == generation {
			return receipt.Lease, true
		}
	}
	return WorktreeLease{}, false
}

// RecordLeaseReturned preserves the result identity after verified pool release.
func (task Task) RecordLeaseReturned(generation string, expected WorktreeLease) (Task, error) {
	current, found := task.RetiredLease(generation)
	if !found || expected == (WorktreeLease{}) || !validWorktreeLease(expected) {
		return Task{}, ErrInvalidTransition
	}
	returned := expected
	returned.State = LeaseReturned
	if current == returned {
		return task, nil
	}
	if current != expected || current.State != LeaseHeld {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	if snapshot.Receipt != nil && snapshot.Receipt.Generation == generation {
		snapshot.Receipt.Lease = returned
	}
	for index := range snapshot.PriorReceipts {
		if snapshot.PriorReceipts[index].Generation == generation {
			snapshot.PriorReceipts[index].Lease = returned
		}
	}
	for index := range snapshot.AbandonedLaunches {
		if snapshot.AbandonedLaunches[index].Generation == generation {
			snapshot.AbandonedLaunches[index].Lease = returned
		}
	}
	return newTransition(snapshot)
}

// RecordAbandonedLease captures an acquisition published before its response was lost.
func (task Task) RecordAbandonedLease(generation string, lease WorktreeLease) (Task, error) {
	receipt, found := task.LaunchAbandonment(generation)
	if !found {
		return Task{}, ErrInvalidTransition
	}
	if receipt.Lease != (WorktreeLease{}) {
		if receipt.Lease == lease {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	if receipt.Worktree != "" || receipt.LeaseHolder == "" || receipt.BaseCommit == "" ||
		!validWorktreeLease(lease) ||
		lease.State != LeaseHeld ||
		lease.Holder != receipt.LeaseHolder ||
		lease.Project != receipt.Project ||
		lease.Branch != receipt.Branch ||
		lease.BaseCommit != receipt.BaseCommit {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	for index := range snapshot.AbandonedLaunches {
		if snapshot.AbandonedLaunches[index].Generation == generation {
			snapshot.AbandonedLaunches[index].Lease = lease
			snapshot.AbandonedLaunches[index].Worktree = lease.Path
		}
	}
	return newTransition(snapshot)
}

func (store *Store) RecordAbandonedLease(
	ctx context.Context,
	taskID, generation string,
	lease WorktreeLease,
) (Task, error) {
	return store.reserveCheckedResources(ctx, taskID,
		func(task Task) (Task, error) { return task.RecordAbandonedLease(generation, lease) },
		func(tasks []Task, candidate Task) error {
			return checkAbandonedLeaseClaim(tasks, candidate, generation, lease)
		},
	)
}

func checkAbandonedLeaseClaim(
	tasks []Task,
	candidate Task,
	generation string,
	lease WorktreeLease,
) error {
	if err := checkWorkerIdentity(tasks, candidate); err != nil {
		return err
	}
	for _, task := range tasks {
		if task.ID() == candidate.ID() {
			task = candidate
		}
		snapshot := task.Snapshot()
		if snapshot.Worker != nil && snapshot.Worker.Worktree == lease.Path {
			return ErrWorkerConflict
		}
		if snapshot.LaunchIntent != nil && snapshot.LaunchIntent.Worktree == lease.Path {
			return ErrWorkerConflict
		}
		if snapshot.Receipt != nil && snapshot.Receipt.Lease.State == LeaseHeld &&
			snapshot.Receipt.Lease.Path == lease.Path {
			return ErrWorkerConflict
		}
		for _, receipt := range snapshot.PriorReceipts {
			if receipt.Lease.State == LeaseHeld && receipt.Lease.Path == lease.Path {
				return ErrWorkerConflict
			}
		}
		for _, receipt := range snapshot.AbandonedLaunches {
			if task.ID() == candidate.ID() && receipt.Generation == generation {
				continue
			}
			if receipt.Lease.State == LeaseHeld && receipt.Lease.Path == lease.Path {
				return ErrWorkerConflict
			}
		}
	}
	return nil
}
