package taskops

import (
	"context"
	"fmt"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/worktree"
)

// WorkerTools fixes the executable dependencies used by hosted workers.
type WorkerTools struct {
	Commands CommandPaths
	Pool     worktree.PoolClient
}

func NewWithWorkerTools(
	root string,
	recipient inbox.Recipient,
	tools WorkerTools,
) (*Service, error) {
	if tools.Pool.Executable != "" {
		if _, err := firstmate.ParseAbsolutePath(tools.Pool.Executable); err != nil {
			return nil, err
		}
	}
	service, err := NewWithWorkerCommands(root, recipient, tools.Commands)
	if err != nil {
		return nil, err
	}
	service.pool = tools.Pool
	return service, nil
}

func checkWorktreeLease(
	ctx context.Context,
	lease taskstate.WorktreeLease,
	pool worktree.PoolClient,
) error {
	if lease == (taskstate.WorktreeLease{}) {
		return nil
	}
	current, found, err := pool.Find(ctx, lease.Project, lease.Holder)
	if err != nil {
		return err
	}
	if !found || current.ID != lease.ID || current.Path != lease.Path ||
		current.Holder != lease.Holder {
		return fmt.Errorf("%w: recorded treehouse lease changed", worktree.ErrIdentity)
	}
	return nil
}
