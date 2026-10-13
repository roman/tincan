package hostservice

import (
	"testing"

	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestAdoptionResponseMatchesConfirmedBinding(t *testing.T) {
	input := &taskops.AdoptionInput{
		TaskID:     "task-1",
		Generation: "gen-1",
		Name:       "worker",
		Session:    "work",
		Pane:       "pane-1",
		Project:    "/repo",
		Worktree:   "/worktree",
		AgentArgs:  []string{"resume", "thread-1"},
	}
	request := Request{Operation: Adopt, Adoption: input}
	worker := taskstate.Worker{
		Name:       input.Name,
		Harness:    "codex",
		Session:    input.Session,
		Pane:       input.Pane,
		Project:    input.Project,
		Worktree:   input.Worktree,
		Generation: input.Generation,
		AgentArgs:  input.AgentArgs,
		Workspace:  "workspace-1",
		Tab:        "tab-1",
	}
	snapshot := taskstate.Snapshot{
		Definition: *intakeRequest().Intake,
		State:      taskstate.Queued,
		Worker:     &worker,
	}
	for _, state := range []taskstate.State{taskstate.Queued, taskstate.InFlight} {
		copy := snapshot
		copy.State = state
		if err := validateResponse(Response{Outcome: Completed, Transition: &copy}, request); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name  string
		alter func(*taskstate.Snapshot)
	}{
		{"wrong task", func(s *taskstate.Snapshot) { s.Definition.ID = "other" }},
		{"wrong generation", func(s *taskstate.Snapshot) { s.Worker.Generation = "other" }},
		{"wrong session", func(s *taskstate.Snapshot) { s.Worker.Session = "other" }},
		{"wrong args", func(s *taskstate.Snapshot) { s.Worker.AgentArgs = []string{"resume", "other"} }},
		{"pending", func(s *taskstate.Snapshot) {
			s.Worker.PendingAdoption = true
			s.Worker.Workspace = ""
			s.Worker.Tab = ""
		}},
		{"missing worker", func(s *taskstate.Snapshot) { s.Worker = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := worker
			s := snapshot
			s.Worker = &w
			test.alter(&s)
			if err := validateResponse(Response{Outcome: Completed, Transition: &s}, request); err == nil {
				t.Fatal("accepted wrong adoption")
			}
		})
	}
	if err := validateResponse(Response{Outcome: Uncertain, Error: "publication unconfirmed"}, request); err != nil {
		t.Fatal(err)
	}
	if err := validateResponse(Response{Outcome: Failed, Error: "failed", Transition: &snapshot}, request); err == nil {
		t.Fatal("accepted adoption success data on failure")
	}
}
