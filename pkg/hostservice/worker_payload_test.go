package hostservice

import (
	"testing"

	"github.com/roman/tincan/pkg/taskops"
)

func TestWorkerPayloadsRejectMixedOperations(t *testing.T) {
	adoption := &taskops.AdoptionInput{
		TaskID:     "task-1",
		Generation: "gen-1",
		Name:       "worker",
		Session:    "work",
		Pane:       "pane-1",
		Project:    "/repo",
		Worktree:   "/worktree",
		AgentArgs:  []string{"resume", "thread-1"},
	}
	assignment := &AssignmentRequest{TaskID: "task-1", Body: "Work"}
	for _, request := range []Request{
		{Operation: Adopt},
		{Operation: SendAssignment},
		{Operation: Adopt, Adoption: adoption, Assignment: assignment},
		{Operation: SendAssignment, Assignment: assignment, Intake: intakeRequest().Intake},
		{Operation: Intake, Adoption: adoption},
		{Operation: SendAssignment, Assignment: &AssignmentRequest{TaskID: "../escape", Body: "Work"}},
		{Operation: SendAssignment, Assignment: &AssignmentRequest{TaskID: "task-1", Body: "  "}},
	} {
		if err := validateRequest(request); err == nil {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	}
}
