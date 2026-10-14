package taskstate

import (
	"encoding/hex"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

// LaunchPhase records an external effect before its submission.
type LaunchPhase string

const (
	LaunchReserved       LaunchPhase = "reserved"
	LaunchPreparing      LaunchPhase = "preparing"
	LaunchLeaseRequested LaunchPhase = "lease_requested"
	LaunchLeaseReady     LaunchPhase = "lease_ready"
	LaunchPaneRequested  LaunchPhase = "pane_requested"
	LaunchAgentRequested LaunchPhase = "agent_requested"
)

// LaunchEndpoint preserves the actual complete location returned by Herdr.
type LaunchEndpoint struct {
	Workspace string `json:"workspace"`
	Tab       string `json:"tab"`
	Pane      string `json:"pane"`
}

// LaunchIntent keeps initial launch recovery independent of the primary session.
type LaunchIntent struct {
	Lease       WorktreeLease   `json:"lease,omitzero"`
	LeaseHolder string          `json:"lease_holder,omitempty"`
	Generation  string          `json:"generation"`
	Harness     string          `json:"harness"`
	Name        string          `json:"name"`
	Session     string          `json:"session"`
	Project     string          `json:"project"`
	Worktree    string          `json:"worktree"`
	Branch      string          `json:"branch"`
	BaseCommit  string          `json:"base_commit"`
	Workspace   string          `json:"workspace"`
	Label       string          `json:"label"`
	AgentArgs   []string        `json:"agent_args,omitempty"`
	Body        string          `json:"body"`
	Phase       LaunchPhase     `json:"phase"`
	Endpoint    *LaunchEndpoint `json:"endpoint,omitempty"`
	ResumeArgs  []string        `json:"resume_args,omitempty"`
}

func cloneLaunchIntent(input *LaunchIntent) *LaunchIntent {
	if input == nil {
		return nil
	}
	copy := *input
	copy.AgentArgs = slices.Clone(input.AgentArgs)
	copy.ResumeArgs = slices.Clone(input.ResumeArgs)
	if input.Endpoint != nil {
		endpoint := *input.Endpoint
		copy.Endpoint = &endpoint
	}
	return &copy
}

// ReserveInitialLaunch fixes the request before any Git or Herdr effect.
func (task Task) ReserveInitialLaunch(input LaunchIntent) (Task, error) {
	if input.Phase != "" || input.Endpoint != nil || len(input.ResumeArgs) != 0 ||
		input.Lease != (WorktreeLease{}) ||
		input.LeaseHolder != "" {
		return Task{}, ErrInvalidTransition
	}
	input.Phase = LaunchReserved
	if task.record.LaunchIntent != nil {
		current := *cloneLaunchIntent(task.record.LaunchIntent)
		current.Phase, current.Endpoint, current.ResumeArgs = LaunchReserved, nil, nil
		if current.LeaseHolder != "" {
			current.Worktree = ""
		}
		current.Lease, current.LeaseHolder = WorktreeLease{}, ""
		if reflect.DeepEqual(current, input) {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	if task.State() != Queued || task.record.Worker != nil || task.record.Completion != nil ||
		task.record.Hold != nil {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.LaunchIntent = &input
	return newTransition(snapshot)
}

// BeginLaunchPreparation marks possible worktree effects before performing them.
func (task Task) BeginLaunchPreparation(generation string) (Task, error) {
	return task.advanceInitialLaunch(generation, LaunchReserved, LaunchPreparing)
}

// RequestLaunchPane records a potentially ambiguous pane creation attempt.
func (task Task) RequestLaunchPane(generation string) (Task, error) {
	intent := task.record.LaunchIntent
	if intent == nil || intent.Worktree == "" {
		return Task{}, ErrInvalidTransition
	}
	previous := LaunchPreparing
	if intent.Lease != (WorktreeLease{}) {
		previous = LaunchLeaseReady
	}
	return task.advanceInitialLaunch(generation, previous, LaunchPaneRequested)
}

// RecordLaunchEndpoint stores a verified location without replacing prior identity.
func (task Task) RecordLaunchEndpoint(generation string, endpoint LaunchEndpoint) (Task, error) {
	intent := task.record.LaunchIntent
	if intent == nil || intent.Generation != generation ||
		(intent.Phase != LaunchPaneRequested && intent.Phase != LaunchAgentRequested) {
		return Task{}, ErrInvalidTransition
	}
	if intent.Endpoint != nil {
		if *intent.Endpoint == endpoint {
			return task, nil
		}
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.LaunchIntent.Endpoint = &endpoint
	return newTransition(snapshot)
}

// RequestLaunchAgent marks native startup before submitting it to the pane.
func (task Task) RequestLaunchAgent(generation string) (Task, error) {
	if task.record.LaunchIntent == nil || task.record.LaunchIntent.Generation != generation ||
		task.record.LaunchIntent.Endpoint == nil {
		return Task{}, ErrInvalidTransition
	}
	return task.advanceInitialLaunch(generation, LaunchPaneRequested, LaunchAgentRequested)
}

func (task Task) advanceInitialLaunch(generation string, previous, next LaunchPhase) (Task, error) {
	intent := task.record.LaunchIntent
	if intent == nil || intent.Generation != generation || task.record.Hold != nil ||
		task.State() != Queued {
		return Task{}, ErrInvalidTransition
	}
	if intent.Phase == next {
		return task, nil
	}
	if intent.Phase != previous {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.LaunchIntent.Phase = next
	return newTransition(snapshot)
}

// RecordLaunchResume captures caller options and the actual native session identity.
func (task Task) RecordLaunchResume(generation string, arguments []string) (Task, error) {
	intent := task.record.LaunchIntent
	if intent == nil || intent.Generation != generation || intent.Phase != LaunchAgentRequested ||
		len(arguments) == 0 {
		return Task{}, ErrInvalidTransition
	}
	if len(intent.ResumeArgs) != 0 {
		if slices.Equal(intent.ResumeArgs, arguments) {
			return task, nil
		}
		selector := "resume"
		if intent.Harness == "claude" {
			selector = "--resume"
		}
		expected := append(slices.Clone(intent.AgentArgs), intent.ResumeArgs...)
		if len(intent.ResumeArgs) != 2 || intent.ResumeArgs[0] != selector ||
			len(intent.AgentArgs) == 0 ||
			!slices.Equal(arguments, expected) {
			return Task{}, ErrInvalidTransition
		}
	}
	snapshot := task.Snapshot()
	snapshot.LaunchIntent.ResumeArgs = slices.Clone(arguments)
	if snapshot.Worker != nil {
		snapshot.Worker.AgentArgs = slices.Clone(arguments)
	}
	return newTransition(snapshot)
}

// CompleteInitialLaunch clears intent after the caller confirms durable dispatch.
func (task Task) CompleteInitialLaunch(generation string) (Task, error) {
	worker := task.record.Worker
	if task.State() != InFlight || worker == nil || worker.Generation != generation ||
		worker.PendingAdoption || worker.PendingGeneration != "" || worker.PendingLaunch != "" || task.record.Completion != nil {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	if intent := snapshot.LaunchIntent; intent != nil {
		snapshot.Worker.Lease = intent.Lease
		if intent.Workspace == "" {
			snapshot.Worker.OwnedWorkspaceLabel = intent.Label
		}
	}
	snapshot.LaunchIntent = nil
	return newTransition(snapshot)
}

// CancelInitialLaunch releases only a reservation with no possible external effects.
func (task Task) CancelInitialLaunch(generation string) (Task, error) {
	intent := task.record.LaunchIntent
	if intent == nil || intent.Generation != generation || intent.Phase != LaunchReserved ||
		task.record.Worker != nil {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	snapshot.LaunchIntent = nil
	return newTransition(snapshot)
}

// AbandonInitialLaunch clears an unbound intent after external absence verification.
// The host must exclude launch execution and verify Herdr resources before applying it.
func (task Task) AbandonInitialLaunch(generation string) (Task, error) {
	if _, ok := task.LaunchAbandonment(generation); ok {
		return task, nil
	}
	if firstmate.ValidateSpawnGeneration(generation) != nil || task.State() != Queued ||
		task.record.Worker != nil {
		return Task{}, ErrInvalidTransition
	}
	intent := task.record.LaunchIntent
	if intent == nil || intent.Generation != generation {
		return Task{}, ErrInvalidTransition
	}
	snapshot := task.Snapshot()
	project := ""
	baseCommit := ""
	if intent.LeaseHolder != "" || intent.Worktree == "" {
		project = intent.Project
		baseCommit = intent.BaseCommit
	}
	snapshot.AbandonedLaunches = append(
		snapshot.AbandonedLaunches,
		LaunchAbandonment{
			BaseCommit:  baseCommit,
			Generation:  generation,
			Worktree:    intent.Worktree,
			Branch:      intent.Branch,
			Lease:       intent.Lease,
			LeaseHolder: intent.LeaseHolder,
			Project:     project,
		},
	)
	snapshot.LaunchIntent = nil
	return newTransition(snapshot)
}

// LaunchAbandonment returns a detached receipt for a previously abandoned generation.
func (task Task) LaunchAbandonment(generation string) (LaunchAbandonment, bool) {
	for _, receipt := range task.record.AbandonedLaunches {
		if receipt.Generation == generation {
			return receipt, true
		}
	}
	return LaunchAbandonment{}, false
}

func validLaunchIntent(selected record) bool {
	intent := selected.LaunchIntent
	if intent == nil {
		return true
	}
	if (selected.State != Queued && selected.State != InFlight) || selected.Completion != nil ||
		firstmate.ValidateSpawnGeneration(intent.Generation) != nil ||
		(intent.Harness != "codex" && intent.Harness != "claude") ||
		!validAtom(
			intent.Name,
			false,
		) || (intent.Workspace != "" && !validAtom(intent.Workspace, false)) || !validAtom(intent.Label, false) ||
		!singleLine(intent.Branch) || strings.HasPrefix(intent.Branch, "-") ||
		!utf8.ValidString(intent.Body) || strings.ContainsRune(intent.Body, 0) {
		return false
	}
	if _, err := herdr.ParseTarget(intent.Session, "pane"); err != nil {
		return false
	}
	if !validWorktreeLease(intent.Lease) || !optionalLine(intent.LeaseHolder) {
		return false
	}
	if intent.Lease != (WorktreeLease{}) &&
		(intent.Lease.State != LeaseHeld || intent.Lease.Holder != intent.LeaseHolder || intent.Lease.Project != intent.Project || intent.Lease.Path != intent.Worktree || intent.Lease.Branch != intent.Branch || intent.Lease.BaseCommit != intent.BaseCommit) {
		return false
	}
	paths := []string{intent.Project}
	if intent.Worktree != "" {
		paths = append(paths, intent.Worktree)
	} else if intent.Phase != LaunchReserved && intent.Phase != LaunchPreparing && intent.Phase != LaunchLeaseRequested {
		return false
	}
	for _, path := range paths {
		if _, err := firstmate.ParseAbsolutePath(path); err != nil {
			return false
		}
	}
	if (len(intent.BaseCommit) != 40 && len(intent.BaseCommit) != 64) ||
		strings.ToLower(intent.BaseCommit) != intent.BaseCommit {
		return false
	}
	if _, err := hex.DecodeString(intent.BaseCommit); err != nil {
		return false
	}
	for _, argument := range append(slices.Clone(intent.AgentArgs), intent.ResumeArgs...) {
		if !singleLine(argument) {
			return false
		}
	}
	switch intent.Phase {
	case LaunchReserved, LaunchPreparing:
		if intent.Endpoint != nil || intent.Lease != (WorktreeLease{}) || intent.LeaseHolder != "" {
			return false
		}
	case LaunchLeaseRequested:
		if intent.Endpoint != nil || intent.Worktree != "" || intent.Lease != (WorktreeLease{}) ||
			intent.LeaseHolder == "" {
			return false
		}
	case LaunchLeaseReady:
		if intent.Endpoint != nil || intent.Lease == (WorktreeLease{}) {
			return false
		}
	case LaunchPaneRequested:
	case LaunchAgentRequested:
		if intent.Endpoint == nil {
			return false
		}
	default:
		return false
	}
	if intent.Phase != LaunchLeaseRequested && intent.Lease == (WorktreeLease{}) &&
		intent.LeaseHolder != "" {
		return false
	}
	if len(intent.ResumeArgs) != 0 && intent.Phase != LaunchAgentRequested {
		return false
	}
	if endpoint := intent.Endpoint; endpoint != nil {
		if !validAtom(endpoint.Workspace, false) ||
			(intent.Workspace != "" && endpoint.Workspace != intent.Workspace) ||
			!validAtom(endpoint.Tab, true) {
			return false
		}
		if _, err := herdr.ParseTarget(intent.Session, endpoint.Pane); err != nil {
			return false
		}
	}
	if worker := selected.Worker; worker != nil {
		if intent.Phase != LaunchAgentRequested || len(intent.ResumeArgs) == 0 ||
			worker.Name != intent.Name || worker.Harness != intent.Harness || worker.Session != intent.Session ||
			worker.Project != intent.Project || worker.Worktree != intent.Worktree || worker.Generation != intent.Generation ||
			worker.Pane != intent.Endpoint.Pane || !slices.Equal(worker.AgentArgs, intent.ResumeArgs) ||
			worker.PendingGeneration != "" || worker.PendingLaunch != "" {
			return false
		}
		if worker.Workspace != "" &&
			(worker.Workspace != intent.Endpoint.Workspace || worker.Tab != intent.Endpoint.Tab) {
			return false
		}
	}
	if selected.Receipt != nil && selected.Receipt.Generation == intent.Generation {
		return false
	}
	for _, receipt := range selected.PriorReceipts {
		if receipt.Generation == intent.Generation {
			return false
		}
	}
	return true
}

func validLaunchAbandonments(selected record) bool {
	seen := map[string]bool{}
	for _, receipt := range selected.AbandonedLaunches {
		if firstmate.ValidateSpawnGeneration(receipt.Generation) != nil ||
			seen[receipt.Generation] ||
			!singleLine(receipt.Branch) ||
			strings.HasPrefix(receipt.Branch, "-") {
			return false
		}
		if receipt.BaseCommit != "" {
			if (len(receipt.BaseCommit) != 40 && len(receipt.BaseCommit) != 64) ||
				strings.ToLower(receipt.BaseCommit) != receipt.BaseCommit {
				return false
			}
			if _, err := hex.DecodeString(receipt.BaseCommit); err != nil {
				return false
			}
			if receipt.Lease != (WorktreeLease{}) &&
				receipt.Lease.BaseCommit != receipt.BaseCommit {
				return false
			}
		}
		if !validWorktreeLease(receipt.Lease) || !optionalLine(receipt.LeaseHolder) {
			return false
		}
		if receipt.Lease == (WorktreeLease{}) && receipt.Worktree != "" &&
			(receipt.Project != "" || receipt.LeaseHolder != "") {
			return false
		}
		if receipt.Worktree != "" {
			if _, err := firstmate.ParseAbsolutePath(receipt.Worktree); err != nil {
				return false
			}
		} else if _, err := firstmate.ParseAbsolutePath(receipt.Project); err != nil {
			return false
		}
		if receipt.Lease != (WorktreeLease{}) &&
			(receipt.Lease.Project != receipt.Project || receipt.Lease.Path != receipt.Worktree || receipt.Lease.Holder != receipt.LeaseHolder || receipt.Lease.Branch != receipt.Branch) {
			return false
		}
		seen[receipt.Generation] = true
	}
	generations := []string{}
	if worker := selected.Worker; worker != nil {
		generations = append(
			generations,
			worker.Generation,
			worker.PendingGeneration,
			worker.PendingLaunch,
		)
	}
	if selected.LaunchIntent != nil {
		generations = append(generations, selected.LaunchIntent.Generation)
	}
	if selected.Completion != nil {
		generations = append(generations, selected.Completion.Generation)
	}
	if selected.Receipt != nil {
		generations = append(generations, selected.Receipt.Generation)
	}
	for _, receipt := range selected.PriorReceipts {
		generations = append(generations, receipt.Generation)
	}
	for _, generation := range generations {
		if seen[generation] {
			return false
		}
	}
	return true
}
