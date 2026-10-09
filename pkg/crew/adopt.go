package crew

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"syscall"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

// Adoption names a live worker and the exact launch details needed for recovery.
type Adoption struct {
	TaskID    string
	Session   string
	Pane      string
	Name      string
	Project   string
	Worktree  string
	Kind      string
	AgentArgs []string
}

// AdoptLocalTask reserves a worker, publishes local task metadata, and completes its crew record.
// A retry completes a pending reservation after an interrupted publication.
func (registry *Registry) AdoptLocalTask(
	ctx context.Context,
	input Adoption,
	client herdr.Client,
) error {
	if err := ValidateLaunch(input.Name, input.AgentArgs); err != nil {
		return err
	}
	id, err := firstmate.ParseTaskID(input.TaskID)
	if err != nil {
		return err
	}
	for _, path := range []string{input.Project, input.Worktree} {
		if _, err := firstmate.ParseAbsolutePath(path); err != nil {
			return err
		}
	}
	target, err := herdr.ParseTarget(input.Session, input.Pane)
	if err != nil {
		return err
	}
	bound, err := client.ForSession(input.Session)
	if err != nil {
		return err
	}
	return registry.withLockedTask(id.String(), func(root *os.Root, check func() error) error {
		if pending, err := completionPending(root, id); err != nil {
			return err
		} else if pending {
			return ErrConflict
		}
		if recovered, err := registry.recoverAdoption(input, id, root, check); recovered ||
			err != nil {
			return err
		}
		return registry.adoptLocked(ctx, input, id, target, bound, root, check)
	})
}

func (registry *Registry) recoverAdoption(
	input Adoption,
	id firstmate.TaskID,
	root *os.Root,
	check func() error,
) (bool, error) {
	reserved, err := registry.Get(id.String())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !reserved.PendingAdoption {
		return false, nil
	}
	metadata, err := firstmate.ReadTaskFromRoot(root, id)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if reserved.TaskID != input.TaskID || reserved.Name != input.Name ||
		reserved.HerdrSession != input.Session || reserved.PaneID != input.Pane ||
		reserved.Project != input.Project || reserved.Worktree != input.Worktree ||
		!slices.Equal(reserved.AgentArgs, input.AgentArgs) ||
		!matchesTask(reserved, metadata) || metadata.Kind() != input.Kind {
		return false, ErrConflict
	}
	completed := reserved
	completed.PendingAdoption = false
	return true, registry.registerAfter(completed, check)
}

// CancelPendingAdoption releases a reservation only when task metadata is absent.
func (registry *Registry) CancelPendingAdoption(taskID string) error {
	id, err := firstmate.ParseTaskID(taskID)
	if err != nil {
		return err
	}
	return registry.withLockedTask(taskID, func(taskRoot *os.Root, check func() error) error {
		if _, err := firstmate.ReadTaskFromRoot(taskRoot, id); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return ErrConflict
		}
		crewRoot, err := registry.openCrew()
		if err != nil {
			return err
		}
		defer crewRoot.Close()
		lock, err := crewRoot.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := acquireLock(lock); err != nil {
			return err
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		record, err := readRecord(crewRoot, taskID)
		if err != nil {
			return err
		}
		if !record.PendingAdoption {
			return ErrConflict
		}
		if err := check(); err != nil {
			return err
		}
		if err := crewRoot.Remove(taskID + ".json"); err != nil {
			return err
		}
		return syncRoot(crewRoot)
	})
}

func (registry *Registry) adoptLocked(
	ctx context.Context,
	input Adoption,
	id firstmate.TaskID,
	target herdr.Target,
	bound herdr.Client,
	root *os.Root,
	check func() error,
) error {
	details, err := bound.PaneDetails(ctx, input.Pane)
	if err != nil {
		return err
	}
	if details.ForegroundDirectory != input.Worktree {
		return fmt.Errorf("live pane directory differs from the requested worktree")
	}
	agent, err := bound.Get(ctx, input.Name)
	if err != nil {
		return err
	}
	if agent.PaneID != input.Pane || agent.Kind == "" || agent.Status == herdr.Done {
		return fmt.Errorf("worker identity or state differs from the requested pane")
	}
	if health := bound.Health(ctx, target); health.Endpoint != herdr.Alive {
		return fmt.Errorf("worker endpoint is %s", health.Endpoint)
	}
	previous, err := firstmate.ReadTaskFromRoot(root, id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	generation := ""
	if err == nil {
		generation = previous.SpawnGeneration()
	} else {
		reserved, readErr := registry.Get(id.String())
		switch {
		case readErr == nil:
			generation = reserved.SpawnGeneration
		case errors.Is(readErr, os.ErrNotExist):
			generation, err = newAdoptionGeneration()
			if err != nil {
				return err
			}
		default:
			return readErr
		}
	}
	data := []byte(fmt.Sprintf(
		"window=%s:%s\nendpoint_task_id=%s\nworktree=%s\nproject=%s\nharness=%s\nkind=%s\nspawn_gen=%s\nbackend=herdr\nherdr_session=%s\nherdr_workspace_id=%s\nherdr_tab_id=%s\nherdr_pane_id=%s\n",
		input.Session,
		input.Pane,
		id,
		input.Worktree,
		input.Project,
		agent.Kind,
		input.Kind,
		generation,
		input.Session,
		details.WorkspaceID,
		details.TabID,
		input.Pane,
	))
	if _, err := firstmate.Parse(data, id); err != nil {
		return err
	}
	if previous.SpawnGeneration() != "" &&
		!sameAdoptionBinding(previous, input, details, agent.Kind) {
		return firstmate.ErrMetadataConflict
	}
	return registry.registerAfter(Record{
		TaskID: id.String(), Name: input.Name, Kind: agent.Kind, PaneID: input.Pane,
		HerdrSession: input.Session, Project: input.Project, Worktree: input.Worktree,
		SpawnGeneration: generation, AgentArgs: input.AgentArgs,
	}, func() error {
		if err := check(); err != nil {
			return err
		}
		if previous.SpawnGeneration() == "" {
			_, err := firstmate.CreateTaskMetadataLocked(root, id, data)
			return err
		}
		return nil
	})
}

func sameAdoptionBinding(
	previous firstmate.Metadata,
	input Adoption,
	details herdr.PaneDetails,
	harness string,
) bool {
	return previous.Project().String() == input.Project &&
		previous.Worktree().String() == input.Worktree &&
		previous.Harness() == harness && previous.Kind() == input.Kind &&
		previous.HerdrSession() == input.Session && previous.HerdrPaneID() == input.Pane &&
		previous.HerdrWorkspaceID() == details.WorkspaceID && previous.HerdrTabID() == details.TabID
}

func newAdoptionGeneration() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "gen-" + hex.EncodeToString(value), nil
}
