package taskops

import (
	"context"
	"errors"
	"fmt"

	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
	"github.com/roman/tincan/pkg/worktree"
)

// ReleaseWorktree returns a retired generation's lease after an explicit request.
func (service *Service) ReleaseWorktree(
	ctx context.Context,
	taskID, generation string,
) (taskstate.Task, error) {
	if err := service.recoverAbandonedLease(ctx, taskID, generation); err != nil {
		return taskstate.Task{}, err
	}
	updated, err := service.store.Update(
		ctx,
		taskID,
		func(task taskstate.Task) (taskstate.Task, error) {
			return service.returnRetiredLease(ctx, task, generation)
		},
	)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}

func (service *Service) returnRetiredLease(
	ctx context.Context,
	task taskstate.Task,
	generation string,
) (taskstate.Task, error) {
	lease, found := task.RetiredLease(generation)
	if !found || lease == (taskstate.WorktreeLease{}) {
		return taskstate.Task{}, taskstate.ErrInvalidTransition
	}
	returned, err := task.RecordLeaseReturned(generation, lease)
	if err != nil {
		return taskstate.Task{}, err
	}
	if lease.State == taskstate.LeaseReturned {
		return returned, nil
	}
	current, found, err := service.pool.Find(ctx, lease.Project, lease.Holder)
	if err != nil {
		return taskstate.Task{}, err
	}
	if found && current.ID == lease.ID && current.Path == lease.Path {
		specification := worktree.Specification{
			Project:    lease.Project,
			Path:       lease.Path,
			Branch:     lease.Branch,
			BaseCommit: lease.BaseCommit,
		}
		verify := service.git.VerifyReturn
		if _, abandoned := task.LaunchAbandonment(generation); abandoned {
			verify = service.git.VerifyUnusedLeaseReturn
		}
		if err := verify(ctx, specification); err != nil {
			return taskstate.Task{}, err
		}
	}
	if err := service.pool.Return(ctx, lease.Project, worktree.Lease{ID: lease.ID, Holder: lease.Holder, Path: lease.Path}); err != nil {
		return taskstate.Task{}, err
	}
	return returned, nil
}

func (service *Service) recoverAbandonedLease(
	ctx context.Context,
	taskID, generation string,
) error {
	task, err := service.store.Read(ctx, taskID)
	if err != nil {
		return err
	}
	receipt, found := task.LaunchAbandonment(generation)
	if !found || receipt.Lease != (taskstate.WorktreeLease{}) {
		return nil
	}
	if receipt.LeaseHolder == "" || receipt.BaseCommit == "" {
		return taskstate.ErrInvalidTransition
	}
	lease, found, err := service.pool.Find(ctx, receipt.Project, receipt.LeaseHolder)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf(
			"abandoned acquisition remains unresolved for holder %q; inspect the pool",
			receipt.LeaseHolder,
		)
	}
	_, err = service.store.RecordAbandonedLease(
		ctx,
		taskID,
		generation,
		taskstate.WorktreeLease{
			ID:         lease.ID,
			Holder:     lease.Holder,
			Project:    receipt.Project,
			Path:       lease.Path,
			Branch:     receipt.Branch,
			BaseCommit: receipt.BaseCommit,
			State:      taskstate.LeaseHeld,
		},
	)
	return err
}
