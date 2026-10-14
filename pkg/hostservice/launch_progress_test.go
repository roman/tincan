package hostservice

import (
	"context"
	"errors"
	"github.com/roman/tincan/pkg/worktree"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/primary"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestTrackedLaunchOutcomeSurvivesHostTransport(t *testing.T) {
	_, _, socket := fixture(t, func(server *Server) {
		server.authorize = func(primary.Caller) error { return nil }
		server.config.Session = "work"
		git := filepath.Join(t.TempDir(), "git")
		if err := os.WriteFile(git, []byte("#!/bin/sh\ncase \"$3\" in\ncheck-ref-format) exit 0 ;;\ncat-file) printf 'commit\\n'; exit 0 ;;\nesac\nexit 72\n"), 0700); err != nil {
			t.Fatal(err)
		}
		server.config.GitExecutable = git
		if err := os.WriteFile(server.config.HerdrExecutable, []byte("#!/bin/sh\nprintf '%s\\n' '{\"result\":{\"workspace\":{\"workspace_id\":\"w1\"}}}'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		task, err := taskstate.NewQueued(
			taskstate.Definition{ID: "task-1", Title: "Work", Repo: "repo", Kind: "task"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.store.Create(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	})
	input := LaunchRequest{
		TaskID:     "task-1",
		Harness:    "codex",
		Project:    "/repo",
		Worktree:   "/worker",
		Branch:     "task-1",
		BaseCommit: strings.Repeat("a", 40),
		Workspace:  "w1",
		Body:       "Do work",
	}
	response, err := Call(context.Background(), socket, Request{Operation: Launch, Launch: &input})
	if err != nil || response.Outcome != Pending || response.Transition == nil ||
		response.Transition.LaunchIntent == nil {
		t.Fatalf("tracked outcome lost over host connection: %+v, %v", response, err)
	}
}

func TestLaunchProgressRejectsMismatchedAndUnrelatedState(t *testing.T) {
	server, input := launchRequestFixture(t)
	task, err := server.reserveInitialLaunch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := task.Snapshot()
	request := Request{Operation: Launch, Launch: &input}
	valid := Response{
		Outcome:    Pending,
		Transition: &snapshot,
		Error:      "native startup remains pending",
	}
	if err := validateResponse(valid, request); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Response){
		func(r *Response) { r.Transition = nil },
		func(r *Response) {
			copy := *r.Transition
			intent := *copy.LaunchIntent
			intent.Body = "different"
			copy.LaunchIntent = &intent
			r.Transition = &copy
		},
		func(r *Response) { r.Error = "" },
		func(r *Response) { r.Status = &StatusResult{} },
		func(r *Response) { r.Outcome = Completed },
	} {
		response := valid
		change(&response)
		if err := validateResponse(response, request); err == nil {
			t.Fatal("accepted mismatched, incomplete or contradictory launch progress")
		}
	}
	if err := validateResponse(valid, Request{Operation: Start, Transition: &TransitionRequest{TaskID: input.TaskID}}); err == nil {
		t.Fatal("accepted pending outcome for unrelated operation")
	}
}

func TestLaunchProgressRequiresMatchingRecoveryAdmission(t *testing.T) {
	server, input := launchRequestFixture(t)
	ctx := context.Background()
	if _, err := server.reserveInitialLaunch(ctx, input); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("native startup awaits inspection")
	if _, err := server.trackedLaunchResult(ctx, input, cause); !errors.Is(err, cause) {
		t.Fatalf("untracked launch: %v", err)
	}
	key := controllerKey{kind: taskController, id: input.TaskID}
	if err := server.controllers.awaitInitialResident(key); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.Body = "different"
	if _, err := server.trackedLaunchResult(ctx, changed, cause); !errors.Is(err, cause) {
		t.Fatalf("changed request became pending: %v", err)
	}
	response, err := server.trackedLaunchResult(ctx, input, cause)
	if err != nil || response.Outcome != Pending {
		t.Fatalf("matching tracked launch: %+v, %v", response, err)
	}
}

func TestTrackedLaunchIdentityMismatchReportsBlocked(t *testing.T) {
	for _, mode := range []string{"pane moved", "agent mismatch", "foreign session", "missing session", "native blocked"} {
		t.Run(mode, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			input.Worktree = t.TempDir()
			t.Setenv("LAUNCH_TEST_WORKTREE", input.Worktree)
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
				if _, err := server.store.Update(ctx, input.TaskID, advance); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := server.store.RecordLaunchEndpoint(ctx, input.TaskID, generation, taskstate.LaunchEndpoint{Workspace: input.Workspace, Tab: "t1", Pane: "p1"}); err != nil {
				t.Fatal(err)
			}
			if _, err := server.store.Update(ctx, input.TaskID, func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchAgent(generation) }); err != nil {
				t.Fatal(err)
			}
			git := filepath.Join(t.TempDir(), "git")
			if err := os.WriteFile(git, []byte("#!/bin/sh\ncase \"$3\" in\ncheck-ref-format) exit 0 ;;\ncat-file) printf 'commit\\n'; exit 0 ;;\nesac\nexit 72\n"), 0700); err != nil {
				t.Fatal(err)
			}
			server.config.GitExecutable = git
			t.Setenv("LAUNCH_TEST_MODE", mode)
			script := `#!/bin/sh
case "$1 $2" in
'pane get')
 workspace=workspace-1
 [ "$LAUNCH_TEST_MODE" != 'pane moved' ] || workspace=other
 printf '{"result":{"pane":{"pane_id":"p1","workspace_id":"%s","tab_id":"t1","foreground_cwd":"%s"}}}' "$workspace" "$LAUNCH_TEST_WORKTREE" ;;
 'agent get')
 case "$LAUNCH_TEST_MODE" in
 'foreign session') printf '%s\n' '{"result":{"agent":{"pane_id":"p1","agent":"codex","agent_status":"working","agent_session":{"source":"other","agent":"codex","kind":"id","value":"native"}}}}' ;;
 'missing session') printf '%s\n' '{"result":{"agent":{"pane_id":"p1","agent":"codex","agent_status":"working"}}}' ;;
 'native blocked') printf '%s\n' '{"result":{"agent":{"pane_id":"p1","agent":"codex","agent_status":"blocked"}}}' ;;
 *) printf '%s\n' '{"result":{"agent":{"pane_id":"different","agent":"codex","agent_status":"working"}}}' ;;
 esac ;;

*) exit 72 ;;
esac
`
			if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			response, err := server.launch(ctx, input)
			want := Blocked
			if mode == "missing session" {
				want = Pending
			}
			if err != nil || response.Outcome != want || response.Transition == nil {
				t.Fatalf("identity mismatch should be blocked: %+v, %v", response, err)
			}
		})
	}
}

func TestTrackedLaunchRemainsInspectableAfterCallerDeadline(t *testing.T) {
	server, input := launchRequestFixture(t)
	ctx := context.Background()
	if _, err := server.reserveInitialLaunch(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := server.controllers.awaitInitialResident(controllerKey{kind: taskController, id: input.TaskID}); err != nil {
		t.Fatal(err)
	}
	expired, cancel := context.WithCancel(ctx)
	cancel()
	response, err := server.trackedLaunchResult(expired, input, context.DeadlineExceeded)
	if err != nil || response.Outcome != Pending || response.Transition == nil {
		t.Fatalf("deadline hid recorded launch progress: %+v, %v", response, err)
	}
}

func TestLaunchRegistrationFailureRetainsConfirmedWorker(t *testing.T) {
	for _, state := range []string{"stopped", "pending", "active", "active conflict"} {
		t.Run(state, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			input.TaskID, input.Worktree = "task-1", "/worktree"
			ctx := context.Background()
			task, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := task.Snapshot()
			confirmed := Response{Transition: &snapshot}
			key := controllerKey{kind: taskController, id: input.TaskID}
			endpoint := controllerEndpoint{
				session: snapshot.Worker.Session,
				pane:    snapshot.Worker.Pane,
			}
			if state == "pending" {
				if err := server.controllers.awaitInitialResident(key); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(state, "active") {
				startEndpointResident(t, server, key, endpoint, func() error { return nil })
				if state == "active" {
					server.controllers.mu.Lock()
					server.controllers.entries[key].specification = supervisionSpecification(
						SupervisionRequest{TaskID: input.TaskID},
						server.config.Supervision,
					)
					server.controllers.mu.Unlock()
				}
			}
			response, err := server.launchRegistrationFailure(
				input,
				confirmed,
				context.Canceled,
			)
			if err != nil {
				t.Fatal(err)
			}
			want := Blocked
			if state == "pending" {
				want = Pending
			}
			if state == "active" {
				want = ""
			}
			if response.Outcome != want || response.Transition == nil ||
				response.Transition.Worker == nil {
				t.Fatalf("lost confirmed worker or supervision state: %+v", response)
			}
			if state != "active" {
				if err := validateResponse(response, Request{Operation: Launch, Launch: &input}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestTrackedLaunchResourceConflictsReportBlocked(t *testing.T) {
	for _, mode := range []string{"existing agent", "conflicting worktree"} {
		t.Run(mode, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			input.Project = t.TempDir()
			input.Worktree = filepath.Join(t.TempDir(), "worker")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			git, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			server.config.GitExecutable = git
			run := func(args ...string) string {
				t.Helper()
				output, err := exec.Command(git, append([]string{"-C", input.Project}, args...)...).
					CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s, %v", args, output, err)
				}
				return strings.TrimSpace(string(output))
			}
			run("init", "-b", "main")
			run(
				"-c",
				"user.name=Test",
				"-c",
				"user.email=test@example.invalid",
				"commit",
				"--allow-empty",
				"-m",
				"Initial",
			)
			input.BaseCommit = run("rev-parse", "HEAD")
			if mode == "conflicting worktree" {
				if err := os.Mkdir(input.Worktree, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := (worktree.Client{Executable: git}).Prepare(ctx, worktree.Specification{Project: input.Project, Path: input.Worktree, Branch: input.Branch, BaseCommit: input.BaseCommit}); err != nil {
					t.Fatal(err)
				}
				task, err := server.reserveInitialLaunch(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				generation := task.Snapshot().LaunchIntent.Generation
				for _, advance := range []func(taskstate.Task) (taskstate.Task, error){
					func(task taskstate.Task) (taskstate.Task, error) { return task.BeginLaunchPreparation(generation) },
					func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchPane(generation) },
				} {
					if _, err := server.store.Update(ctx, input.TaskID, advance); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := server.store.RecordLaunchEndpoint(ctx, input.TaskID, generation, taskstate.LaunchEndpoint{Workspace: input.Workspace, Tab: "t1", Pane: "p1"}); err != nil {
					t.Fatal(err)
				}
				t.Setenv("LAUNCH_TEST_WORKTREE", input.Worktree)
				script := `#!/bin/sh
case "$1 $2" in
'pane get') printf '{"result":{"pane":{"pane_id":"p1","workspace_id":"workspace-1","tab_id":"t1","foreground_cwd":"%s"}}}' "$LAUNCH_TEST_WORKTREE" ;;
'agent get') printf '%s\n' '{"result":{"agent":{"pane_id":"p1","agent":"codex","agent_status":"working"}}}' ;;
*) exit 72 ;;
esac
`
				if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			response, err := server.launch(ctx, input)
			if err != nil || response.Outcome != Blocked || response.Transition == nil {
				t.Fatalf("resource conflict should be blocked: %+v, %v", response, err)
			}
		})
	}
}

func TestTrackedLaunchHeldOrMissingWorkspaceReportsBlocked(t *testing.T) {
	for _, mode := range []string{"held", "missing workspace"} {
		t.Run(mode, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			task, err := server.reserveInitialLaunch(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			generation := task.Snapshot().LaunchIntent.Generation
			git := filepath.Join(t.TempDir(), "git")
			if err := os.WriteFile(git, []byte("#!/bin/sh\ncase \"$3\" in\ncheck-ref-format) exit 0 ;;\ncat-file) printf 'commit\\n'; exit 0 ;;\nesac\nexit 72\n"), 0700); err != nil {
				t.Fatal(err)
			}
			server.config.GitExecutable = git
			if mode == "held" {
				if _, err := server.store.Update(ctx, input.TaskID, func(task taskstate.Task) (taskstate.Task, error) {
					return task.PlaceHold(taskstate.Hold{Reason: "Wait"})
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, advance := range []func(taskstate.Task) (taskstate.Task, error){
					func(task taskstate.Task) (taskstate.Task, error) { return task.BeginLaunchPreparation(generation) },
					func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchPane(generation) },
				} {
					if _, err := server.store.Update(ctx, input.TaskID, advance); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(server.config.HerdrExecutable, []byte("#!/bin/sh\nprintf '%s\\n' '{\"error\":{\"code\":\"workspace_not_found\",\"message\":\"missing workspace\"}}'\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			response, err := server.launch(ctx, input)
			if err != nil || response.Outcome != Blocked || response.Transition == nil {
				t.Fatalf("tracked block should remain explicit: %+v, %v", response, err)
			}
		})
	}
}
