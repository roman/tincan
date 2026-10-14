package worktree

import (
	"context"
	"fmt"
	"strings"
)

// PrepareLeased creates the task branch within an existing clean lease.
func (client Client) PrepareLeased(ctx context.Context, specification Specification) error {
	if err := client.Validate(ctx, specification); err != nil {
		return err
	}
	sourceRoot, err := client.git(ctx, specification.Project, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if samePath(strings.TrimSuffix(sourceRoot, "\n"), specification.Path) {
		return fmt.Errorf("%w: the source checkout cannot be a worker lease", ErrIdentity)
	}
	registered, err := client.registration(ctx, specification)
	if err != nil {
		return err
	}
	branch := "refs/heads/" + specification.Branch
	if registered == nil || registered.locked || registered.stale ||
		(registered.branch != "" && registered.branch != branch) {
		return fmt.Errorf(
			"%w: lease must be a detached registered worktree or the reserved task branch",
			ErrIdentity,
		)
	}
	if err := client.verifyRepository(ctx, specification); err != nil {
		return err
	}
	if err := client.checkCheckoutIndex(ctx, specification); err != nil {
		return err
	}
	status, err := client.git(
		ctx,
		specification.Path,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
	)
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%w: leased worktree has changed or untracked files", ErrIdentity)
	}
	if registered.branch == "" {
		if _, err := client.git(ctx, specification.Path, "switch", "--no-overwrite-ignore", "-c", specification.Branch, specification.BaseCommit); err != nil {
			return err
		}
	}
	return client.verifyPrepared(ctx, specification)
}
