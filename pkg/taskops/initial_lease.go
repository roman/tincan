package taskops

import (
	"context"
	"fmt"

	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/worktree"
)

func (service *Service) prepareInitialLease(
	ctx context.Context,
	taskID string,
	intent *taskstate.LaunchIntent,
	pool worktree.PoolClient,
) (*taskstate.LaunchIntent, error) {
	generation := intent.Generation
	acquire := intent.Phase == taskstate.LaunchPreparing && intent.Worktree == ""
	if acquire {
		holder := fmt.Sprintf("concierge:%s:%s:%s", service.root, taskID, generation)
		if _, err := service.store.RequestLaunchLease(ctx, taskID, generation, holder); err != nil {
			return nil, err
		}
		var err error
		_, intent, err = service.initialLaunch(ctx, taskID, generation)
		if err != nil {
			return nil, err
		}
	}
	if intent.Phase != taskstate.LaunchLeaseRequested {
		return intent, nil
	}
	if err := service.checkInitialLaunchActive(ctx, taskID, generation); err != nil {
		return nil, err
	}
	var lease worktree.Lease
	var err error
	if acquire {
		lease, err = pool.Acquire(ctx, intent.Project, intent.LeaseHolder)
	} else {
		var found bool
		lease, found, err = pool.Find(ctx, intent.Project, intent.LeaseHolder)
		if err == nil && !found {
			err = fmt.Errorf("treehouse acquisition remains unresolved for holder %q; inspect the pool before abandonment", intent.LeaseHolder)
		}
	}
	if err != nil {
		return nil, err
	}
	captured := taskstate.WorktreeLease{
		ID:         lease.ID,
		Holder:     lease.Holder,
		Path:       lease.Path,
		Project:    intent.Project,
		Branch:     intent.Branch,
		BaseCommit: intent.BaseCommit,
		State:      taskstate.LeaseHeld,
	}
	if _, err := service.store.RecordLaunchLease(ctx, taskID, generation, captured); err != nil {
		return nil, err
	}
	_, intent, err = service.initialLaunch(ctx, taskID, generation)
	return intent, err
}

func checkInitialLease(
	ctx context.Context,
	intent *taskstate.LaunchIntent,
	pool worktree.PoolClient,
) error {
	if err := checkWorktreeLease(ctx, intent.Lease, pool); err != nil {
		return fmt.Errorf("%w: %w", ErrInitialLaunchIdentity, err)
	}
	return nil
}
