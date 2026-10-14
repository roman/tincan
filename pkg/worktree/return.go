package worktree

import (
	"context"
	"fmt"
	"strings"
)

// VerifyReturn protects a result checkout before the pool parks it on another commit.
func (client Client) VerifyReturn(ctx context.Context, specification Specification) error {
	if err := client.VerifyIdentity(ctx, specification); err != nil {
		return err
	}
	return client.verifyReturnContents(ctx, specification)
}

func (client Client) verifyReturnContents(ctx context.Context, specification Specification) error {
	if err := client.checkCheckoutIndex(ctx, specification); err != nil {
		return err
	}
	// A reset can remove .gitignore before cleaning files that were ignored.
	status, err := client.git(
		ctx,
		specification.Path,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
		"--ignored=matching",
	)
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%w: return requires a clean checkout without ignored files", ErrIdentity)
	}
	return nil
}

// VerifyUnusedLeaseReturn also permits a detached acquisition with a reachable commit.
func (client Client) VerifyUnusedLeaseReturn(
	ctx context.Context,
	specification Specification,
) error {
	if err := client.Validate(ctx, specification); err != nil {
		return err
	}
	source, err := client.git(ctx, specification.Project, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if samePath(strings.TrimSuffix(source, "\n"), specification.Path) {
		return ErrIdentity
	}
	registered, err := client.registration(ctx, specification)
	if err != nil {
		return err
	}
	if registered == nil || registered.locked || registered.stale ||
		(registered.branch != "" && registered.branch != "refs/heads/"+specification.Branch) {
		return ErrIdentity
	}
	if err := client.verifyRepository(ctx, specification); err != nil {
		return err
	}
	if registered.branch == "" {
		references, err := client.git(
			ctx,
			specification.Path,
			"for-each-ref",
			"--contains=HEAD",
			"--format=%(refname)",
			"refs/heads",
			"refs/remotes",
			"refs/tags",
		)
		if err != nil {
			return err
		}
		if strings.TrimSpace(references) == "" {
			return fmt.Errorf(
				"%w: detached work is not reachable from a durable reference",
				ErrIdentity,
			)
		}
	} else if err := client.VerifyIdentity(ctx, specification); err != nil {
		return err
	}
	return client.verifyReturnContents(ctx, specification)
}
