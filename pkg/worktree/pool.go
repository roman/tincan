package worktree

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Lease identifies one durable treehouse acquisition.
type Lease struct {
	Path   string `json:"path"`
	ID     string `json:"lease_id"`
	Holder string `json:"lease_holder"`
}

type PoolClient struct{ Executable string }

type poolEntry struct {
	Lease
	Flavor         string `json:"flavor"`
	RecoveryReason string `json:"recovery_reason"`
	BranchError    string `json:"branch_error"`
	Processes      *[]struct {
		PID int `json:"pid"`
	} `json:"processes"`
}

func cleanPoolPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && poolText(path)
}

func poolText(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validLeaseForProject(project string, lease Lease) bool {
	return cleanPoolPath(project) && cleanPoolPath(lease.Path) && poolText(lease.ID) &&
		poolText(lease.Holder) &&
		!samePath(lease.Path, project)
}

func (client PoolClient) run(
	ctx context.Context,
	project string,
	arguments ...string,
) ([]byte, error) {
	if !cleanPoolPath(client.Executable) || !cleanPoolPath(project) {
		return nil, fmt.Errorf("treehouse executable and project must be clean absolute paths")
	}
	command := exec.CommandContext(ctx, client.Executable, arguments...)
	command.Dir = project
	output, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf(
				"treehouse %s: %w: %s",
				arguments[0],
				err,
				strings.TrimSpace(string(exit.Stderr)),
			)
		}
		return nil, fmt.Errorf("treehouse %s: %w", arguments[0], err)
	}
	return output, nil
}

func (client PoolClient) entries(ctx context.Context, project string) ([]poolEntry, error) {
	output, err := client.run(ctx, project, "status", "--json")
	if err != nil {
		return nil, err
	}
	var entries []poolEntry
	if json.Unmarshal(output, &entries) != nil || entries == nil {
		return nil, fmt.Errorf("%w: invalid treehouse JSON inventory", ErrIdentity)
	}
	paths := map[string]bool{}
	for _, entry := range entries {
		if !cleanPoolPath(entry.Path) || paths[entry.Path] || entry.Processes == nil {
			return nil, fmt.Errorf("%w: incomplete or duplicate treehouse inventory", ErrIdentity)
		}
		paths[entry.Path] = true
	}
	return entries, nil
}

// Find recovers a holder's existing acquisition without creating another lease.
func (client PoolClient) Find(ctx context.Context, project, holder string) (Lease, bool, error) {
	if !poolText(holder) {
		return Lease{}, false, ErrIdentity
	}
	entries, err := client.entries(ctx, project)
	if err != nil {
		return Lease{}, false, err
	}
	var found Lease
	for _, entry := range entries {
		if entry.Holder != holder {
			continue
		}
		if !validLeaseForProject(project, entry.Lease) || found.ID != "" || entry.Flavor != "git" ||
			entry.RecoveryReason != "" ||
			entry.BranchError != "" {
			return Lease{}, false, fmt.Errorf(
				"%w: ambiguous or damaged treehouse lease",
				ErrIdentity,
			)
		}
		found = entry.Lease
	}
	return found, found.ID != "", nil
}

// Acquire confirms the returned identity against treehouse's durable inventory.
// The caller must record the request before this non-idempotent external effect.
func (client PoolClient) Acquire(ctx context.Context, project, holder string) (Lease, error) {
	if !poolText(holder) {
		return Lease{}, ErrIdentity
	}
	output, err := client.run(
		ctx,
		project,
		"get",
		"--lease",
		"--json",
		"--no-fetch",
		"--lease-holder",
		holder,
	)
	if err != nil {
		return Lease{}, err
	}
	var lease Lease
	if json.Unmarshal(output, &lease) != nil || !validLeaseForProject(project, lease) ||
		lease.Holder != holder ||
		lease.Path == project {
		return Lease{}, fmt.Errorf("%w: invalid acquired treehouse lease", ErrIdentity)
	}
	confirmed, found, err := client.Find(ctx, project, holder)
	if err != nil {
		return Lease{}, err
	}
	if !found || confirmed != lease {
		return Lease{}, fmt.Errorf("%w: treehouse acquisition is unconfirmed", ErrIdentity)
	}
	return lease, nil
}

// Return releases only the recorded, quiet lease and verifies the postcondition.
// Dirty worktrees receive EOF rather than approval to discard their changes.
func (client PoolClient) Return(ctx context.Context, project string, lease Lease) error {
	if !validLeaseForProject(project, lease) {
		return ErrIdentity
	}
	entries, err := client.entries(ctx, project)
	if err != nil {
		return err
	}
	var selected *poolEntry
	for index := range entries {
		if entries[index].Path == lease.Path {
			selected = &entries[index]
		}
	}
	if selected == nil {
		return fmt.Errorf("%w: recorded treehouse worktree is absent", ErrIdentity)
	}
	if selected.ID == "" && selected.Holder == "" {
		return nil
	}
	if selected.Lease != lease || selected.Flavor != "git" || selected.RecoveryReason != "" ||
		selected.BranchError != "" ||
		len(*selected.Processes) != 0 {
		return fmt.Errorf("%w: lease changed, damaged, or has live processes", ErrIdentity)
	}
	if _, err := client.run(ctx, project, "return", lease.Path, "--if-lease-id", lease.ID, "--if-lease-holder", lease.Holder); err != nil {
		return err
	}
	entries, err = client.entries(ctx, project)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Path == lease.Path && entry.ID == lease.ID {
			return fmt.Errorf("%w: treehouse lease release is unconfirmed", ErrIdentity)
		}
	}
	return nil
}
