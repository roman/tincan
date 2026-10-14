package hostservice

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func TestAbandonFailedLaunchKeepsTaskAndResources(t *testing.T) {
	server, input := launchRequestFixture(t)
	ctx := context.Background()
	task, err := server.reserveInitialLaunch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	generation := task.Snapshot().LaunchIntent.Generation
	for _, advance := range []func(taskstate.Task) (taskstate.Task, error){
		func(task taskstate.Task) (taskstate.Task, error) { return task.BeginLaunchPreparation(generation) },
		func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchPane(generation) },
	} {
		task, err = server.store.Update(ctx, input.TaskID, advance)
		if err != nil {
			t.Fatal(err)
		}
	}
	before := task.Snapshot()
	if err := server.controllers.awaitInitialResident(controllerKey{kind: taskController, id: input.TaskID}); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1 $2" in
'workspace list') printf '%s\n' '{"result":{"workspaces":[{"workspace_id":"w1"}]}}' ;;
'tab list') printf '%s\n' '{"result":{"tabs":[]}}' ;;
'agent get') printf '%s\n' '{"error":{"code":"agent_not_found","message":"absent"}}'; exit 1 ;;
*) exit 72 ;;
esac
`
	if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	request := Request{
		Operation:  Operation("abandon_launch"),
		Transition: &TransitionRequest{TaskID: input.TaskID, Generation: generation},
	}
	response, err := server.perform(ctx, request)
	if err != nil {
		t.Fatalf("abandon failed launch: %v", err)
	}
	after, err := server.store.Read(ctx, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	expected := before
	expected.LaunchIntent = nil
	expected.AbandonedLaunches = []taskstate.LaunchAbandonment{
		{Generation: generation, Worktree: input.Worktree, Branch: input.Branch},
	}
	if !reflect.DeepEqual(after.Snapshot(), expected) {
		t.Fatalf("abandon changed task or retained intent: %+v", after.Snapshot())
	}
	if response.Transition == nil {
		t.Fatal("missing task result")
	}
	retried, err := server.perform(ctx, request)
	if err != nil {
		t.Fatalf("abandon retry: %v", err)
	}
	for _, result := range []*AbandonedLaunch{response.Abandoned, retried.Abandoned} {
		if result == nil || result.Worktree != input.Worktree || result.Branch != input.Branch {
			t.Fatalf("lost retained paths: %+v", result)
		}
	}
	if len(server.controllers.initialResidentTasks()) != 0 {
		t.Fatal("abandoned task remains eligible for recovery")
	}
}

func TestAbandonLaunchRefusesLiveOrUncertainResources(t *testing.T) {
	for _, test := range []struct{ name, mode string }{
		{"registered worker", "agent"}, {"launch tab", "tab"}, {"renamed tab with same pane", "pane"},
		{"malformed inventory", "malformed"}, {"failed inventory", "failed"}, {"wrong generation", "stale"}, {"active launch", "busy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			task, err := server.reserveInitialLaunch(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			generation := task.Snapshot().LaunchIntent.Generation
			for _, advance := range []func(taskstate.Task) (taskstate.Task, error){
				func(task taskstate.Task) (taskstate.Task, error) { return task.BeginLaunchPreparation(generation) },
				func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchPane(generation) },
			} {
				task, err = server.store.Update(ctx, input.TaskID, advance)
				if err != nil {
					t.Fatal(err)
				}
			}
			task, err = server.store.RecordLaunchEndpoint(
				ctx,
				input.TaskID,
				generation,
				taskstate.LaunchEndpoint{Workspace: input.Workspace, Tab: "t1", Pane: "p1"},
			)
			if err != nil {
				t.Fatal(err)
			}
			task, err = server.store.Update(
				ctx,
				input.TaskID,
				func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchAgent(generation) },
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := server.controllers.awaitInitialResident(controllerKey{kind: taskController, id: input.TaskID}); err != nil {
				t.Fatal(err)
			}
			before := task.Snapshot()
			t.Setenv("ABANDON_MODE", test.mode)
			t.Setenv("ABANDON_LABEL", before.LaunchIntent.Label)
			script := `#!/bin/sh
case "$1 $2" in
'agent get')
 if [ "$ABANDON_MODE" = agent ]; then printf '%s\n' '{"result":{"agent":{"pane_id":"p1","agent_status":"working","agent":"codex"}}}'; else printf '%s\n' '{"error":{"code":"agent_not_found"}}'; exit 1; fi ;;
'workspace list')
 case "$ABANDON_MODE" in
 malformed) printf '{}';; failed) exit 72;; *) printf '%s\n' '{"result":{"workspaces":[{"workspace_id":"workspace-1"},{"workspace_id":"other"}]}}';; esac ;;
'tab list')
 if [ "$4" = workspace-1 ]; then printf '%s\n' '{"result":{"tabs":[]}}'; elif [ "$ABANDON_MODE" = tab ]; then printf '{"result":{"tabs":[{"workspace_id":"other","tab_id":"t1","label":"%s"}]}}' "$ABANDON_LABEL"; else printf '%s\n' '{"result":{"tabs":[{"workspace_id":"other","tab_id":"renamed","label":"other"}]}}'; fi ;;
'pane list') if [ "$4" = workspace-1 ]; then printf '%s\n' '{"result":{"panes":[]}}'; else printf '%s\n' '{"result":{"panes":[{"workspace_id":"other","pane_id":"p1"}]}}'; fi ;;
*) exit 72 ;;
esac
`
			if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if test.mode == "stale" {
				generation = "old-generation"
			}
			if test.mode == "busy" {
				release, err := server.controllers.reserveInitialLaunch(
					controllerKey{kind: taskController, id: input.TaskID},
				)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			_, err = server.perform(
				ctx,
				Request{
					Operation:  Operation("abandon_launch"),
					Transition: &TransitionRequest{TaskID: input.TaskID, Generation: generation},
				},
			)
			if err == nil {
				t.Fatal("abandoned a live, uncertain, stale, or busy launch")
			}
			if len(server.controllers.initialResidentTasks()) != 1 {
				t.Fatal("failed abandonment disabled recovery")
			}

			after, err := server.store.Read(ctx, input.TaskID)
			if err != nil || !reflect.DeepEqual(before, after.Snapshot()) {
				t.Fatalf("refused abandonment changed task: %v", err)
			}
		})
	}
}

func TestHistoricalAbandonmentRetryPreservesNewLaunch(t *testing.T) {
	server, input := launchRequestFixture(t)
	ctx := context.Background()
	task, err := server.reserveInitialLaunch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	generation := task.Snapshot().LaunchIntent.Generation
	task, err = server.store.Update(
		ctx,
		input.TaskID,
		func(task taskstate.Task) (taskstate.Task, error) { return task.AbandonInitialLaunch(generation) },
	)
	if err != nil {
		t.Fatal(err)
	}
	task, err = server.reserveInitialLaunch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	before := task.Snapshot()
	if err := server.controllers.awaitInitialResident(controllerKey{kind: taskController, id: input.TaskID}); err != nil {
		t.Fatal(err)
	}
	response, err := server.perform(
		ctx,
		Request{
			Operation:  AbandonLaunch,
			Transition: &TransitionRequest{TaskID: input.TaskID, Generation: generation},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	after, err := server.store.Read(ctx, input.TaskID)
	if err != nil || !reflect.DeepEqual(before, after.Snapshot()) {
		t.Fatalf("historical retry changed new launch: %v", err)
	}
	if len(server.controllers.initialResidentTasks()) != 1 {
		t.Fatal("historical retry disabled new recovery")
	}
	if response.Abandoned == nil || response.Abandoned.Worktree != input.Worktree ||
		response.Abandoned.Branch != input.Branch {
		t.Fatalf("lost historical paths: %+v", response.Abandoned)
	}
}

func TestAbandonmentResponseRequiresMatchingDurableReceipt(t *testing.T) {
	server, input := launchRequestFixture(t)
	ctx := context.Background()
	task, err := server.reserveInitialLaunch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	generation := task.Snapshot().LaunchIntent.Generation
	task, err = server.store.Update(
		ctx,
		input.TaskID,
		func(task taskstate.Task) (taskstate.Task, error) { return task.AbandonInitialLaunch(generation) },
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := task.Snapshot()
	receipt, _ := task.LaunchAbandonment(generation)
	request := Request{
		Operation:  AbandonLaunch,
		Transition: &TransitionRequest{TaskID: input.TaskID, Generation: generation},
	}
	valid := Response{Outcome: Completed, Transition: &snapshot, Abandoned: &receipt}
	if err := validateResponse(valid, request); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Response){
		func(r *Response) { r.Abandoned = nil },
		func(r *Response) { copy := *r.Abandoned; copy.Worktree = "/different"; r.Abandoned = &copy },
		func(r *Response) { r.Status = &StatusResult{} },
		func(r *Response) { r.Outcome = Failed; r.Error = "failure" },
		func(r *Response) { copy := *r.Transition; copy.AbandonedLaunches = nil; r.Transition = &copy },
	} {
		response := valid
		change(&response)
		if err := validateResponse(response, request); err == nil {
			t.Fatal("accepted incomplete, contradictory, or unbound abandonment result")
		}
	}
}
