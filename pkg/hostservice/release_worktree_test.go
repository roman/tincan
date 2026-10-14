package hostservice

import (
	"context"
	"fmt"
	"github.com/roman/tincan/pkg/primary"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/worktree"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func TestWorktreeReleaseRequiresGenerationAndReturnedReceipt(t *testing.T) {
	request := Request{
		Operation:  Operation("release_worktree"),
		Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"},
	}
	if err := validateRequest(request); err != nil {
		t.Fatalf("release request: %v", err)
	}
	for _, input := range []TransitionRequest{{TaskID: "task-1"}, {TaskID: "task-1", Generation: "gen-1", Mode: taskstate.Retain}, {TaskID: "task-1", Generation: "bad generation"}} {
		invalid := request
		invalid.Transition = &input
		if err := validateRequest(invalid); err == nil {
			t.Fatalf("accepted invalid release: %+v", input)
		}
	}
	lease := taskstate.WorktreeLease{
		ID:         "lease-1",
		Holder:     "holder",
		Project:    "/repo",
		Path:       "/worker",
		Branch:     "task/work",
		BaseCommit: strings.Repeat("a", 40),
		State:      taskstate.LeaseReturned,
	}
	snapshot := taskstate.Snapshot{
		Definition: taskstate.Definition{ID: "task-1", Title: "Work", Repo: "repo", Kind: "ship"},
		State:      taskstate.Queued,
		Receipt:    &taskstate.Receipt{Generation: "gen-1", Mode: taskstate.Retain, Lease: lease},
	}
	response := Response{Outcome: Completed, Transition: &snapshot}
	if err := validateResponse(response, request); err != nil {
		t.Fatalf("returned lease response: %v", err)
	}
	snapshot.Receipt.Lease.State = taskstate.LeaseHeld
	if err := validateResponse(response, request); err == nil {
		t.Fatal("accepted unreleased lease response")
	}
}

func TestHostReleaseHistoricalLeasePreservesCurrentWorker(t *testing.T) {
	var expected taskstate.Worker
	server, _, socket := fixture(t, func(server *Server) {
		server.authorize = func(primary.Caller) error { return nil }
		project := t.TempDir()
		oldPath := filepath.Join(project, "old-worker")
		executable := filepath.Join(t.TempDir(), "treehouse")
		inventory := fmt.Sprintf(
			`[{"path":%q,"lease_id":"","lease_holder":"","flavor":"git","processes":[]}]`,
			oldPath,
		)
		script := "#!/bin/sh\n[ \"$1\" = status ] && [ \"$2\" = --json ] || exit 72\nprintf '%s' '" + inventory + "'\n"
		if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		service, err := taskops.NewWithWorkerTools(
			server.config.Root,
			server.config.Recipient,
			taskops.WorkerTools{
				Commands: server.config.WorkerCommands,
				Pool:     worktree.PoolClient{Executable: executable},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		server.service = service
		expected = taskstate.Worker{
			Name:       "new-worker",
			Harness:    "codex",
			Session:    "work",
			Workspace:  "w1",
			Tab:        "w1:t1",
			Pane:       "w1:p1",
			Project:    project,
			Worktree:   filepath.Join(project, "current-worker"),
			Generation: "gen-2",
			AgentArgs:  []string{"resume", "native-session"},
		}
		task, err := taskstate.NewQueued(
			taskstate.Definition{ID: "task-1", Title: "Work", Repo: "repo", Kind: "ship"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.store.Create(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		reservation := expected
		reservation.Workspace, reservation.Tab = "", ""
		if _, err := server.store.ReserveAdoption(context.Background(), "task-1", reservation); err != nil {
			t.Fatal(err)
		}
		if _, err := server.store.Update(context.Background(), "task-1", func(task taskstate.Task) (taskstate.Task, error) {
			task, err := task.ConfirmAdoption(expected.Workspace, expected.Tab)
			if err != nil {
				return taskstate.Task{}, err
			}
			task, err = task.Start()
			if err != nil {
				return taskstate.Task{}, err
			}
			snapshot := task.Snapshot()
			snapshot.Receipt = &taskstate.Receipt{Generation: "gen-1", Mode: taskstate.Retain, Lease: taskstate.WorktreeLease{ID: "old-lease", Holder: "old-holder", Project: project, Path: oldPath, Branch: "task/old", BaseCommit: strings.Repeat("a", 40), State: taskstate.LeaseHeld}}
			return taskstate.NewMigrated(snapshot)
		}); err != nil {
			t.Fatal(err)
		}

	})
	request := Request{
		Operation:  ReleaseWorktree,
		Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"},
	}
	response, err := Call(context.Background(), socket, request)
	if err != nil || response.Outcome != Completed {
		t.Fatalf("release transport: %+v; %v", response, err)
	}
	if !reflect.DeepEqual(*response.Transition.Worker, expected) ||
		response.Transition.Receipt.Lease.State != taskstate.LeaseReturned {
		t.Fatalf("historical release changed current worker: %+v", response.Transition)
	}
	task, err := server.store.Read(context.Background(), "task-1")
	if err != nil || task.Snapshot().Receipt.Lease.State != taskstate.LeaseReturned {
		t.Fatalf("release did not persist: %v", err)
	}
}
