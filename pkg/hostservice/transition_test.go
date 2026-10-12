package hostservice

import (
	"context"
	"testing"

	"github.com/roman/tincan/pkg/primary"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestLifecyclePayloadsRejectMixedOrIncompleteOperations(t *testing.T) {
	for _, request := range []Request{
		{Operation: Start},
		{Operation: Start, Transition: &TransitionRequest{TaskID: "../escape"}},
		{Operation: Start, Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"}},
		{Operation: Release, Transition: &TransitionRequest{TaskID: "task-1", Hold: &taskstate.Hold{Reason: "Wait"}}},
		{Operation: Hold, Transition: &TransitionRequest{TaskID: "task-1"}},
		{Operation: Finish, Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1", Mode: taskstate.Close, ClosedOn: "2026-02-30"}},
		{Operation: Finish, Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1", Mode: taskstate.Retain, ClosedOn: "2026-10-12"}},
		{Operation: Hold, Intake: intakeRequest().Intake, Transition: &TransitionRequest{TaskID: "task-1", Hold: &taskstate.Hold{Reason: "Wait"}}},
	} {
		t.Run(string(request.Operation), func(t *testing.T) {
			_, _, socket := fixture(t, func(server *Server) {
				server.authorize = func(primary.Caller) error { t.Error("malformed request reached admission"); return nil }
			})
			response, err := Call(context.Background(), socket, request)
			if err != nil || response.Outcome != Invalid {
				t.Fatalf("response %+v err %v", response, err)
			}
		})
	}
}

func TestLifecycleResponsesMatchRequestedMutation(t *testing.T) {
	worker := &taskstate.Worker{
		Name:       "worker",
		Harness:    "codex",
		Session:    "work",
		Workspace:  "workspace-1",
		Tab:        "tab-1",
		Pane:       "pane-1",
		Project:    "/repo",
		Worktree:   "/worktree",
		Generation: "gen-1",
		AgentArgs:  []string{"resume", "thread-1"},
	}
	queued, err := taskstate.NewMigrated(
		taskstate.Snapshot{
			Definition: *intakeRequest().Intake,
			State:      taskstate.Queued,
			Worker:     worker,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, err := queued.Start()
	if err != nil {
		t.Fatal(err)
	}
	finished, err := started.AcceptResult("gen-1", taskstate.Close, "2026-10-12")
	if err != nil {
		t.Fatal(err)
	}
	hold := taskstate.Hold{Reason: "Wait"}
	held, err := queued.PlaceHold(hold)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name         string
		request      Request
		valid, wrong taskstate.Task
	}{
		{"start", Request{Operation: Start, Transition: &TransitionRequest{TaskID: "task-1"}}, started, queued},
		{"hold", Request{Operation: Hold, Transition: &TransitionRequest{TaskID: "task-1", Hold: &hold}}, held, queued},
		{"release", Request{Operation: Release, Transition: &TransitionRequest{TaskID: "task-1"}}, queued, held},
		{"finish", Request{Operation: Finish, Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1", Mode: taskstate.Close, ClosedOn: "2026-10-12"}}, finished, started},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := test.valid.Snapshot()
			if err := validateResponse(Response{Outcome: Completed, Transition: &snapshot}, test.request); err != nil {
				t.Fatal(err)
			}
			snapshot = test.wrong.Snapshot()
			if err := validateResponse(Response{Outcome: Completed, Transition: &snapshot}, test.request); err == nil {
				t.Fatal("accepted mismatched result")
			}
			if err := validateResponse(Response{Outcome: Completed}, test.request); err == nil {
				t.Fatal("accepted missing result")
			}
		})
	}
	snapshot := finished.Snapshot()
	request := Request{
		Operation: Finish,
		Transition: &TransitionRequest{
			TaskID:     "task-1",
			Generation: "other",
			Mode:       taskstate.Close,
			ClosedOn:   "2026-10-12",
		},
	}
	if err := validateResponse(Response{Outcome: Completed, Transition: &snapshot}, request); err == nil {
		t.Fatal("accepted wrong generation")
	}
	request.Transition.Generation = "gen-1"
	request.Transition.ClosedOn = "2026-10-13"
	if err := validateResponse(Response{Outcome: Completed, Transition: &snapshot}, request); err == nil {
		t.Fatal("accepted wrong closing date")
	}
}

func TestLifecycleRejectsImpossibleSuccessStates(t *testing.T) {
	worker := &taskstate.Worker{
		Name:       "worker",
		Harness:    "codex",
		Session:    "work",
		Workspace:  "workspace-1",
		Tab:        "tab-1",
		Pane:       "pane-1",
		Project:    "/repo",
		Worktree:   "/worktree",
		Generation: "gen-2",
		AgentArgs:  []string{"resume", "thread-1"},
	}
	definition := *intakeRequest().Intake
	hold := taskstate.Hold{Reason: "Wait"}
	base := taskstate.Snapshot{
		Definition: definition,
		State:      taskstate.InFlight,
		Worker:     worker,
		Completion: &taskstate.Completion{
			Generation: "gen-2",
			Mode:       taskstate.Close,
			Phase:      taskstate.Accepted,
		},
	}
	done := base
	done.State = taskstate.Done
	done.Definition.Closed = "2026-10-12"
	heldDone := done
	heldDone.Definition.Hold = &hold
	newer := base
	newer.State = taskstate.Queued
	newer.Definition.Hold = &hold
	newer.Completion = &taskstate.Completion{
		Generation: "gen-2",
		Mode:       taskstate.Retain,
		Phase:      taskstate.Accepted,
	}
	newer.Receipt = &taskstate.Receipt{Generation: "gen-1", Mode: taskstate.Retain}
	newer.PriorReceipts = []taskstate.Receipt{{Generation: "gen-0", Mode: taskstate.Retain}}
	for _, test := range []struct {
		name     string
		request  Request
		snapshot taskstate.Snapshot
	}{
		{"start with completion", Request{Operation: Start, Transition: &TransitionRequest{TaskID: "task-1"}}, base},
		{"hold done", Request{Operation: Hold, Transition: &TransitionRequest{TaskID: "task-1", Hold: &hold}}, heldDone},
		{"release done", Request{Operation: Release, Transition: &TransitionRequest{TaskID: "task-1"}}, done},
		{"finish prior receipt", Request{Operation: Finish, Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-0", Mode: taskstate.Retain}}, newer},
		{"finish current old receipt", Request{Operation: Finish, Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1", Mode: taskstate.Retain}}, newer},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := taskstate.NewMigrated(test.snapshot); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			if err := validateResponse(Response{Outcome: Completed, Transition: &test.snapshot}, test.request); err == nil {
				t.Fatal("accepted impossible success state")
			}
		})
	}
}

func TestLifecycleRejectsWrongIdentityAndMalformedSnapshot(t *testing.T) {
	request := Request{Operation: Release, Transition: &TransitionRequest{TaskID: "task-1"}}
	for _, snapshot := range []taskstate.Snapshot{
		{Definition: taskstate.Definition{ID: "other", Title: "Work", Repo: "exmachina", Kind: "task"}, State: taskstate.Queued},
		{Definition: *intakeRequest().Intake, State: "unknown"},
	} {
		if err := validateResponse(Response{Outcome: Completed, Transition: &snapshot}, request); err == nil {
			t.Fatalf("accepted invalid result: %+v", snapshot)
		}
	}
}
