package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrIdentity = errors.New("worktree identity conflict")

type Client struct {
	Executable string
}

type Specification struct {
	Project    string
	Path       string
	Branch     string
	BaseCommit string
}

func (client Client) git(
	ctx context.Context,
	directory string,
	arguments ...string,
) (string, error) {
	command := exec.CommandContext(
		ctx,
		client.Executable,
		append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf(
			"git %s: %w: %s",
			strings.Join(arguments, " "),
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return string(output), nil
}

// Validate checks the branch and exact commit without changing the repository.
func (client Client) Validate(ctx context.Context, specification Specification) error {
	if !filepath.IsAbs(client.Executable) || !filepath.IsAbs(specification.Project) ||
		!filepath.IsAbs(specification.Path) {
		return fmt.Errorf("git executable, project, and worktree paths must be absolute")
	}
	if specification.Branch == "" || strings.HasPrefix(specification.Branch, "-") ||
		specification.Branch == "HEAD" {
		return fmt.Errorf("a literal branch name is required")
	}
	if _, err := client.git(ctx, specification.Project, "check-ref-format", "refs/heads/"+specification.Branch); err != nil {
		return err
	}
	if len(specification.BaseCommit) != 40 && len(specification.BaseCommit) != 64 {
		return fmt.Errorf("a full base commit ID is required")
	}
	for _, character := range specification.BaseCommit {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return fmt.Errorf("a full base commit ID is required")
		}
	}
	kind, err := client.git(ctx, specification.Project, "cat-file", "-t", specification.BaseCommit)
	if err != nil {
		return err
	}
	if strings.TrimSpace(kind) != "commit" {
		return fmt.Errorf("base object must be a commit in the project")
	}
	return nil
}

type registration struct {
	path   string
	branch string
	locked bool
	stale  bool
}

func samePath(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func (client Client) registration(
	ctx context.Context,
	specification Specification,
) (*registration, error) {
	output, err := client.git(ctx, specification.Project, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var current registration
	for _, field := range strings.Split(output, "\x00") {
		if field == "" {
			if current.path != "" && samePath(current.path, specification.Path) {
				return &current, nil
			}
			current = registration{}
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		switch key {
		case "worktree":
			current.path = value
		case "branch":
			current.branch = value
		case "locked":
			current.locked = true
		case "prunable":
			current.stale = true
		}
	}
	return nil, nil
}

func (client Client) Prepare(ctx context.Context, specification Specification) error {
	if err := client.Validate(ctx, specification); err != nil {
		return err
	}
	registered, err := client.registration(ctx, specification)
	if err != nil {
		return err
	}
	if registered == nil {
		if _, err := os.Lstat(specification.Path); err == nil {
			return fmt.Errorf(
				"%w: worktree path already exists without a matching registration",
				ErrIdentity,
			)
		} else if !os.IsNotExist(err) {
			return err
		}
		// Git's branch creation rejects competing launches through repository aliases.
		if _, err := client.git(ctx, specification.Project, "worktree", "add", "-b", specification.Branch, specification.Path, specification.BaseCommit); err != nil {
			return err
		}
	}
	return client.verifyPrepared(ctx, specification)
}

func (client Client) verifyPrepared(ctx context.Context, specification Specification) error {
	if err := client.VerifyIdentity(ctx, specification); err != nil {
		return err
	}
	for _, lock := range []string{"index.lock", "HEAD.lock"} {
		path, err := client.git(
			ctx,
			specification.Path,
			"rev-parse",
			"--path-format=absolute",
			"--git-path",
			lock,
		)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(strings.TrimSpace(path)); err == nil {
			return fmt.Errorf("worktree initialization lock exists: %s", lock)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	head, err := client.git(ctx, specification.Path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != specification.BaseCommit {
		return fmt.Errorf(
			"%w: initial worktree HEAD differs from the reserved base commit",
			ErrIdentity,
		)
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
		"--untracked-files=no",
	)
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%w: initial worktree checkout is incomplete or changed", ErrIdentity)
	}
	return nil
}

// VerifyIdentity permits commits and unfinished edits after the worker starts.
func (client Client) VerifyIdentity(ctx context.Context, specification Specification) error {
	if err := client.Validate(ctx, specification); err != nil {
		return err
	}
	registered, err := client.registration(ctx, specification)
	if err != nil {
		return err
	}
	if registered == nil || registered.locked || registered.stale ||
		registered.branch != "refs/heads/"+specification.Branch {
		return fmt.Errorf(
			"%w: worktree registration does not match the reserved branch and path",
			ErrIdentity,
		)
	}
	if err := client.verifyRepository(ctx, specification); err != nil {
		return err
	}
	branch, err := client.git(ctx, specification.Path, "symbolic-ref", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(branch) != "refs/heads/"+specification.Branch {
		return fmt.Errorf("%w: worktree branch differs from the reserved branch", ErrIdentity)
	}
	return nil
}

func (client Client) checkCheckoutIndex(ctx context.Context, specification Specification) error {
	index, err := client.git(ctx, specification.Path, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	// Skip-worktree and assume-unchanged flags can hide missing or modified files from status.
	for _, entry := range strings.Split(index, "\x00") {
		if entry != "" && !strings.HasPrefix(entry, "H ") {
			return fmt.Errorf(
				"%w: initial worktree index can hide tracked checkout differences",
				ErrIdentity,
			)
		}
	}
	return nil
}

func (client Client) verifyRepository(ctx context.Context, specification Specification) error {
	projectCommon, err := client.git(
		ctx,
		specification.Project,
		"rev-parse",
		"--path-format=absolute",
		"--git-common-dir",
	)
	if err != nil {
		return err
	}
	workerCommon, err := client.git(
		ctx,
		specification.Path,
		"rev-parse",
		"--path-format=absolute",
		"--git-common-dir",
	)
	if err != nil {
		return err
	}
	if !samePath(strings.TrimSpace(projectCommon), strings.TrimSpace(workerCommon)) {
		return fmt.Errorf("%w: worktree belongs to a different repository", ErrIdentity)
	}
	return nil
}
