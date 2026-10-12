package taskstate

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestQueuedTaskRoundTripPreservesBacklogFields(t *testing.T) {
	input := Definition{
		ID: "task-1", Title: "Ship the task store", Body: "Keep the full task body.\n",
		Repo: "exmachina", Kind: "ship",
		Dependencies: []Dependency{{Type: "blocked-by", ID: "task-0", Reason: "wait for review"}},
		Hold:         &Hold{Reason: "operator review", Kind: "captain", Until: "2026-10-20"},
		Created:      "2026-10-11", Priority: priority(2),
	}
	task, err := NewQueued(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Dependencies[0].ID = "changed"
	input.Hold.Reason = "changed"
	*input.Priority = 4
	encoded, err := task.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.State() != Queued || decoded.ID() != input.ID {
		t.Fatalf("decoded task = %s %s", decoded.ID(), decoded.State())
	}
	if got := decoded.Definition(); got.Body != input.Body || got.Hold == nil ||
		got.Hold.Reason != "operator review" || len(got.Dependencies) != 1 ||
		got.Dependencies[0].ID != "task-0" || *got.Priority != 2 {
		t.Fatalf("decoded definition lost backlog fields: %#v", got)
	}
	copy := decoded.Definition()
	copy.Dependencies[0].ID = "changed again"
	copy.Hold.Reason = "changed again"
	*copy.Priority = 3
	if got := decoded.Definition(); got.Dependencies[0].ID != "task-0" ||
		got.Hold.Reason != "operator review" || *got.Priority != 2 {
		t.Fatalf("task retained caller-owned data: %#v", got)
	}
}

func TestQueuedTaskRejectsInvalidDefinitions(t *testing.T) {
	base := Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"}
	tests := []struct {
		name string
		edit func(*Definition)
	}{
		{"missing id", func(input *Definition) { input.ID = "" }},
		{"missing title", func(input *Definition) { input.Title = "" }},
		{"self dependency", func(input *Definition) {
			input.Dependencies = []Dependency{{Type: "blocked-by", ID: "task-1"}}
		}},
		{"duplicate dependency", func(input *Definition) {
			input.Dependencies = []Dependency{
				{Type: "blocked-by", ID: "task-0"},
				{Type: "blocked-by", ID: "task-0"},
			}
		}},
		{"invalid hold", func(input *Definition) { input.Hold = &Hold{Kind: "captain"} }},
		{"invalid date", func(input *Definition) { input.Created = "tomorrow" }},
		{"invalid priority", func(input *Definition) { input.Priority = priority(5) }},
		{"closed queued task", func(input *Definition) { input.Closed = "2026-10-11" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if _, err := NewQueued(input); !errors.Is(err, ErrInvalidTask) {
				t.Fatalf("NewQueued error = %v, want ErrInvalidTask", err)
			}
		})
	}
}

func TestDecodeRejectsUnknownAndImpossibleFields(t *testing.T) {
	base := []byte(
		`{"format":2,"id":"task-1","title":"Work","repo":"exmachina","kind":"ship","state":"queued"}`,
	)
	tests := []struct {
		name string
		data []byte
	}{
		{
			"unknown field",
			bytes.Replace(
				base,
				[]byte(`"state":"queued"`),
				[]byte(`"state":"queued","mystery":true`),
				1,
			),
		},
		{
			"in flight without worker",
			bytes.Replace(base, []byte(`"state":"queued"`), []byte(`"state":"in_flight"`), 1),
		},
		{"wrong format", bytes.Replace(base, []byte(`"format":2`), []byte(`"format":3`), 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(test.data); !errors.Is(err, ErrInvalidTask) {
				t.Fatalf("Decode error = %v, want ErrInvalidTask", err)
			}
		})
	}
}

func TestMigratedTaskPreservesWorkerLaunchIntent(t *testing.T) {
	input := Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      InFlight,
		Worker: &Worker{
			Name: "worker-1", Harness: "codex", Session: "work", Workspace: "workspace-1",
			Tab: "tab-1", Pane: "pane-1", Project: "/project", Worktree: "/worktree",
			Generation: "gen-1", PendingLaunch: "gen-2", AgentArgs: []string{"resume", "thread-1"},
		},
	}
	task, err := NewMigrated(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Worker.AgentArgs[0] = "changed"
	encoded, err := task.Encode()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Snapshot()
	if got.Worker.PendingLaunch != "gen-2" || got.Worker.AgentArgs[0] != "resume" {
		t.Fatalf("worker launch intent changed: %#v", got.Worker)
	}
	got.Worker.AgentArgs[0] = "changed again"
	if task.Snapshot().Worker.AgentArgs[0] != "resume" {
		t.Fatal("snapshot changed the source task")
	}
}

func TestMigratedTaskRejectsConflictingResultState(t *testing.T) {
	base := Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      InFlight,
		Worker: &Worker{
			Name: "worker-1", Harness: "codex", Session: "work", Workspace: "workspace-1",
			Tab: "tab-1", Pane: "pane-1", Project: "/project", Worktree: "/worktree",
			Generation: "gen-1", AgentArgs: []string{"resume", "thread-1"},
		},
	}
	tests := []struct {
		name string
		edit func(*Snapshot)
	}{
		{"done with retain result", func(snapshot *Snapshot) {
			snapshot.State = Done
			snapshot.Completion = &Completion{Generation: "gen-1", Mode: Retain, Phase: Accepted}
		}},
		{"queued with close result", func(snapshot *Snapshot) {
			snapshot.State = Queued
			snapshot.Completion = &Completion{Generation: "gen-1", Mode: Close, Phase: Accepted}
		}},
		{"in flight during adoption", func(snapshot *Snapshot) {
			snapshot.Worker.PendingAdoption = true
		}},
		{"result during launch", func(snapshot *Snapshot) {
			snapshot.Worker.PendingLaunch = "gen-2"
			snapshot.Completion = &Completion{Generation: "gen-1", Mode: Close, Phase: Accepted}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := Snapshot{
				Definition: base.Definition,
				State:      base.State,
				Worker:     cloneWorker(base.Worker),
			}
			test.edit(&input)
			if _, err := NewMigrated(input); !errors.Is(err, ErrInvalidTask) {
				t.Fatalf("NewMigrated error = %v, want ErrInvalidTask", err)
			}
		})
	}
}

func TestMigratedTaskRepresentsCompletionStages(t *testing.T) {
	definition := Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"}
	worker := &Worker{
		Name: "worker-1", Harness: "codex", Session: "work", Workspace: "workspace-1",
		Tab: "tab-1", Pane: "pane-1", Project: "/project", Worktree: "/worktree",
		Generation: "gen-1", AgentArgs: []string{"resume", "thread-1"},
	}
	tests := []struct {
		name     string
		snapshot Snapshot
	}{
		{"historical done", Snapshot{Definition: definition, State: Done}},
		{"accepted before queue update", Snapshot{
			Definition: definition, State: InFlight, Worker: worker,
			Completion: &Completion{Generation: "gen-1", Mode: Close, Phase: Accepted},
		}},
		{"retired held task", Snapshot{
			Definition: definition, State: Queued, Worker: worker,
			Completion: &Completion{Generation: "gen-1", Mode: Retain, Phase: Retired},
		}},
		{"retired before queue update", Snapshot{
			Definition: definition, State: InFlight, Worker: worker,
			Completion: &Completion{Generation: "gen-1", Mode: Close, Phase: Retired},
		}},
		{"retired receipt after close", Snapshot{
			Definition: definition, State: Done,
			Completion: &Completion{Generation: "gen-1", Mode: Close, Phase: Retired},
		}},
		{"retired receipt after retain", Snapshot{
			Definition: definition, State: Queued,
			Completion: &Completion{Generation: "gen-1", Mode: Retain, Phase: Retired},
		}},
		{"retired receipt before queue update", Snapshot{
			Definition: definition, State: InFlight,
			Completion: &Completion{Generation: "gen-1", Mode: Close, Phase: Retired},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task, err := NewMigrated(test.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := task.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(encoded); err != nil {
				t.Fatalf("stored task could not be loaded: %v", err)
			}
		})
	}
}

func TestMigratedTaskRejectsAcceptedResultWithoutWorker(t *testing.T) {
	input := Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      Done,
		Completion: &Completion{Generation: "gen-1", Mode: Close, Phase: Accepted},
	}
	if _, err := NewMigrated(input); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("NewMigrated error = %v, want invalid task", err)
	}
}

func TestEncodeRejectsRecordBeyondDecodeLimit(t *testing.T) {
	task, err := NewQueued(Definition{
		ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship",
		Body: strings.Repeat("x", maximumTaskBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.Encode(); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Encode error = %v, want ErrInvalidTask", err)
	}
}

func TestMigrationRejectsMalformedWorkerBinding(t *testing.T) {
	base := Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      Queued,
		Worker: &Worker{
			Name: "worker-1", Harness: "codex", Session: "work", Workspace: "workspace-1",
			Tab: "tab-1", Pane: "pane-1", Project: "/project", Worktree: "/worktree",
			Generation: "gen-1", AgentArgs: []string{"resume", "thread-1"},
		},
	}
	tests := []struct {
		name string
		edit func(*Worker)
	}{
		{"harness with spaces", func(worker *Worker) { worker.Harness = "codex agent" }},
		{"pending adoption with invalid workspace", func(worker *Worker) {
			worker.PendingAdoption = true
			worker.Workspace = "bad workspace"
		}},
		{"pending adoption with only workspace", func(worker *Worker) {
			worker.PendingAdoption = true
			worker.Tab = ""
		}},
		{"pending adoption with only tab", func(worker *Worker) {
			worker.PendingAdoption = true
			worker.Workspace = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Worker = cloneWorker(base.Worker)
			test.edit(input.Worker)
			if _, err := NewMigrated(input); !errors.Is(err, ErrInvalidTask) {
				t.Fatalf("NewMigrated error = %v, want ErrInvalidTask", err)
			}
		})
	}
}

func TestMigratedCompletionIsDetachedFromCaller(t *testing.T) {
	input := Snapshot{
		Definition: Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
		State:      InFlight,
		Worker: &Worker{
			Name: "worker-1", Harness: "codex", Session: "work", Workspace: "workspace-1",
			Tab: "tab-1", Pane: "pane-1", Project: "/project", Worktree: "/worktree",
			Generation: "gen-1", AgentArgs: []string{"resume", "thread-1"},
		},
		Completion: &Completion{Generation: "gen-1", Mode: Close, Phase: Accepted},
	}
	task, err := NewMigrated(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Completion.Mode = Retain
	returned := task.Snapshot()
	returned.Completion.Phase = Retired
	if got := task.Snapshot().Completion; got.Mode != Close || got.Phase != Accepted {
		t.Fatalf("caller changed task completion: %#v", got)
	}
}

func TestDecodeRejectsOversizedRecord(t *testing.T) {
	data := append(
		[]byte(
			`{"format":2,"id":"task-1","title":"Work","repo":"exmachina","kind":"ship","state":"queued","body":"`,
		),
		[]byte(strings.Repeat("x", maximumTaskBytes))...)
	data = append(data, []byte(`"}`)...)
	if _, err := Decode(data); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("Decode error = %v, want ErrInvalidTask", err)
	}
}

func priority(value int) *int { return &value }
