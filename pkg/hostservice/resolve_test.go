package hostservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/primary"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestResolutionPreservesCompletedTaskWhenAcknowledgementFails(t *testing.T) {
	server, root, socket := fixture(
		t,
		func(server *Server) { server.authorize = func(primary.Caller) error { return nil } },
	)
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
	task, err := taskstate.NewMigrated(
		taskstate.Snapshot{
			Definition: *intakeRequest().Intake,
			State:      taskstate.InFlight,
			Worker:     worker,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	store, _ := taskstate.NewStore(root)
	data, err := task.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records", "task-1.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	messages, _ := inbox.New(server.config.MessageRoot)
	recipient, _ := inbox.ParseRecipient("task-1")
	assignmentBody, _ := json.Marshal(
		taskprotocol.Assignment{
			Kind:            "task-assignment",
			State:           filepath.Join(root, "tasks"),
			Task:            "task-1",
			SpawnGeneration: "gen-1",
			Body:            "Work",
		},
	)
	assignment, err := messages.Send(recipient, "assignment", string(assignmentBody))
	if err != nil {
		t.Fatal(err)
	}
	replyBody, _ := json.Marshal(
		taskprotocol.Reply{
			Kind:            "task-reply",
			State:           filepath.Join(root, "tasks"),
			Task:            "task-1",
			SpawnGeneration: "gen-1",
			AssignmentID:    assignment.ID.String(),
			ReplyKey:        "answer",
			Body:            "Done",
		},
	)
	key := "task-reply:" + root + ":task-1:gen-1:" + assignment.ID.String()
	reply, err := messages.Send(server.config.Recipient, key, string(replyBody))
	if err != nil {
		t.Fatal(err)
	}
	handled := filepath.Join(
		server.config.MessageRoot,
		"inbox",
		"concierge",
		"handled",
		reply.ID.String()+".json",
	)
	if err := os.Symlink(filepath.Base(handled), handled); err != nil {
		t.Fatal(err)
	}
	request := Request{
		Operation: Resolve,
		Resolve: &ResolveRequest{
			ReplyID:  reply.ID.String(),
			Mode:     taskstate.Close,
			ClosedOn: "2026-10-12",
		},
	}
	response, err := Call(context.Background(), socket, request)
	if err != nil || response.Outcome != Failed || response.Resolution == nil ||
		response.Resolution.TaskID != "task-1" ||
		response.Resolution.State != taskstate.Done {
		t.Fatalf("partial resolution %+v error %v", response, err)
	}
	completed, err := store.Read(context.Background(), "task-1")
	if err != nil || completed.State() != taskstate.Done {
		t.Fatalf("completed record missing: %+v %v", completed.Snapshot(), err)
	}
	if _, err := messages.Read(server.config.Recipient, reply.ID); err != nil {
		t.Fatalf("reply lost after acknowledgement failure: %v", err)
	}
	if err := os.Remove(handled); err != nil {
		t.Fatal(err)
	}
	response, err = Call(context.Background(), socket, request)
	if err != nil || response.Outcome != Completed || response.Resolution == nil ||
		!response.Resolution.Already {
		t.Fatalf("recovery %+v error %v", response, err)
	}
}

func TestPartialResolutionRejectsUnrelatedOrInvalidResults(t *testing.T) {
	request := Request{
		Operation: Resolve,
		Resolve: &ResolveRequest{
			ReplyID:  "k-0000000000000000000000000000000000000000000000000000000000000000",
			Mode:     taskstate.Close,
			ClosedOn: "2026-10-12",
		},
	}
	for _, response := range []Response{
		{Outcome: Failed, Error: "failed", Resolution: &taskops.ReplyResult{TaskID: "../bad", State: taskstate.Done}},
		{Outcome: Uncertain, Error: "uncertain", Resolution: &taskops.ReplyResult{TaskID: "task-1", State: taskstate.Queued}},
	} {
		if err := validateResponse(response, request); err == nil {
			t.Fatalf("accepted invalid partial result %+v", response)
		}
	}
	if err := validateResponse(Response{Outcome: Failed, Error: "failed", Resolution: &taskops.ReplyResult{TaskID: "task-1", State: taskstate.Done}}, intakeRequest()); err == nil {
		t.Fatal("accepted unrelated partial result")
	}
}
