package hostservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func TestLaunchPayloadRejectsMissingMixedAndInvalidRequests(t *testing.T) {
	_, valid := launchRequestFixture(t)
	if err := validateRequest(Request{Operation: Launch, Launch: &valid}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.Launch = nil },
		func(r *Request) { r.Intake = intakeRequest().Intake },
		func(r *Request) { r.Operation = Start },
		func(r *Request) { r.Launch.Body = " " },
		func(r *Request) { r.Launch.TaskID = "../escape" },
		func(r *Request) { r.Launch.Project = "relative" },
		func(r *Request) { r.Launch.Workspace = "" },
		func(r *Request) { r.Launch.BaseCommit = "main" },
	} {
		input := valid
		request := Request{Operation: Launch, Launch: &input}
		change(&request)
		if err := validateRequest(request); err == nil {
			t.Fatalf("accepted invalid request: %+v", request)
		}
	}
}

func TestHostLaunchRejectsMissingToolsBeforeReservation(t *testing.T) {
	server, input := launchRequestFixture(t)
	ctx := context.Background()
	before, _ := server.store.Read(ctx, input.TaskID)
	if _, err := server.launch(ctx, input); err == nil {
		t.Fatal("launch accepted a missing Git executable")
	}
	after, _ := server.store.Read(ctx, input.TaskID)
	if !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
		t.Fatal("missing tool changed task state")
	}
}

func TestHostLaunchRejectsChangedRetryBeforeGit(t *testing.T) {
	server, input := launchRequestFixture(t)
	ctx := context.Background()
	if _, err := server.reserveInitialLaunch(ctx, input); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("LAUNCH_GIT_MARKER", marker)
	git := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nprintf invoked > \"$LAUNCH_GIT_MARKER\"\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server.config.GitExecutable = git
	input.Body = "Different work"
	if _, err := server.launch(ctx, input); !errors.Is(err, taskstate.ErrInvalidTransition) {
		t.Fatalf("changed retry: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("changed retry invoked Git")
	}
}

func TestHostLaunchFailurePreservesIntentAndReleasesReservation(t *testing.T) {
	server, input := launchRequestFixture(t)
	git := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\ncase \"$3\" in\ncheck-ref-format) exit 0 ;;\ncat-file) printf 'commit\\n'; exit 0 ;;\nesac\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server.config.GitExecutable = git
	ctx := context.Background()
	response, err := server.perform(ctx, Request{Operation: Launch, Launch: &input})
	if err == nil || response.Transition != nil {
		t.Fatalf("failed launch response: %+v, %v", response, err)
	}
	task, err := server.store.Read(ctx, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Snapshot().LaunchIntent == nil ||
		task.Snapshot().LaunchIntent.Phase != taskstate.LaunchPreparing {
		t.Fatal("Git failure lost the resumable intent")
	}
	release, err := server.controllers.reserveInitialLaunch(
		controllerKey{kind: taskController, id: input.TaskID},
	)
	if err != nil {
		t.Fatalf("failure leaked task reservation: %v", err)
	}
	release()
}

func TestHostLaunchPreflightFailureLeavesTaskUnchanged(t *testing.T) {
	for _, invalid := range []string{"branch", "missing commit", "blob", "Git invocation"} {
		t.Run(invalid, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			input.Project = t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				output, err := exec.Command("git", append([]string{"-C", input.Project}, args...)...).
					CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s, %v", args, output, err)
				}
				return strings.TrimSpace(string(output))
			}
			git("init", "-b", "main")
			git(
				"-c",
				"user.name=Test",
				"-c",
				"user.email=test@example.invalid",
				"commit",
				"--allow-empty",
				"-m",
				"Initial",
			)
			input.BaseCommit = git("rev-parse", "HEAD")
			server.config.GitExecutable, _ = exec.LookPath("git")
			switch invalid {
			case "branch":
				input.Branch = "bad..branch"
			case "missing commit":
				input.BaseCommit = strings.Repeat("a", 40)
			case "blob":
				input.BaseCommit = git("hash-object", "-w", "--stdin")
			case "Git invocation":
				server.config.GitExecutable = filepath.Join(t.TempDir(), "missing-git")
			}
			ctx := context.Background()
			before, _ := server.store.Read(ctx, input.TaskID)
			if _, err := server.launch(ctx, input); err == nil {
				t.Fatal("invalid preflight accepted")
			}
			after, _ := server.store.Read(ctx, input.TaskID)
			if !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatal("preflight failure reserved or advanced the task")
			}
		})
	}
}

func TestLaunchResponseRequiresConfirmedMatchingWorker(t *testing.T) {
	server, input := launchRequestFixture(t)
	input.TaskID, input.Worktree = "task-1", "/worktree"
	task, _ := server.store.Read(context.Background(), input.TaskID)
	valid := task.Snapshot()
	request := Request{Operation: Launch, Launch: &input}
	if err := validateResponse(Response{Outcome: Completed, Transition: &valid}, request); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []Outcome{Failed, Uncertain} {
		t.Run(string(outcome), func(t *testing.T) {
			if err := validateResponse(Response{Outcome: outcome, Error: "launch failed", Transition: &valid}, request); err == nil {
				t.Fatalf("accepted launch success data with %s outcome", outcome)
			}
		})
	}
	for _, change := range []func(*taskstate.Snapshot){
		func(s *taskstate.Snapshot) { s.State = taskstate.Queued },
		func(s *taskstate.Snapshot) { s.Worker = nil },
		func(s *taskstate.Snapshot) { s.Worker.Worktree = "/different" },
		func(s *taskstate.Snapshot) { s.Worker.Harness = "claude" },
		func(s *taskstate.Snapshot) { s.Worker.PendingLaunch = "next-generation" },
	} {
		snapshot := task.Snapshot()
		change(&snapshot)
		if err := validateResponse(Response{Outcome: Completed, Transition: &snapshot}, request); err == nil {
			t.Fatal("accepted an unapplied or mismatched launch")
		}
	}
}

func launchRequestFixture(t *testing.T) (*Server, LaunchRequest) {
	t.Helper()
	server, _, _ := taskEndpointFixture(t)
	server.config.Session = "work"
	if err := os.WriteFile(server.config.HerdrExecutable, []byte(`#!/bin/sh
case "$1 $2" in
'workspace get') printf '%s\n' '{"result":{"workspace":{"workspace_id":"workspace-1"}}}' ;;
*) exit 72 ;;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	task, err := taskstate.NewQueued(
		taskstate.Definition{ID: "task-2", Title: "Launch", Repo: "repo", Kind: "ship"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.store.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return server, LaunchRequest{
		TaskID:     "task-2",
		Harness:    "codex",
		Project:    "/repo",
		Worktree:   "/worktree-2",
		Branch:     "task-2",
		BaseCommit: strings.Repeat("a", 40),
		Workspace:  "workspace-1",
		AgentArgs:  []string{"--model", "example"},
		Body:       "Do the work",
	}
}

func TestLaunchRequestRetryPreservesIdentityAndProgress(t *testing.T) {
	server, input := launchRequestFixture(t)
	input.Body = " \nDo the work.\n "
	ctx := context.Background()
	first, err := server.reserveInitialLaunch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	intent := first.Snapshot().LaunchIntent
	if intent == nil || intent.Phase != taskstate.LaunchReserved || intent.Generation == "" ||
		intent.Name == "" ||
		intent.Label == "" {
		t.Fatalf("reservation: %+v", intent)
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`).MatchString(intent.Name) {
		t.Fatalf("reserved agent name violates Herdr contract: %q", intent.Name)
	}
	if !strings.HasSuffix(intent.Name, strings.ToLower(intent.Generation)) {
		t.Fatalf("agent name lost generation characters: %+v", intent)
	}
	if intent.Body != input.Body || !reflect.DeepEqual(intent.AgentArgs, input.AgentArgs) {
		t.Fatal("reservation changed the valid assignment body or arguments")
	}
	advanced, err := server.store.Update(
		ctx,
		input.TaskID,
		func(task taskstate.Task) (taskstate.Task, error) {
			return task.BeginLaunchPreparation(intent.Generation)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := server.reserveInitialLaunch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(advanced.Snapshot(), retried.Snapshot()) {
		t.Fatal("retry replaced identity or launch progress")
	}
}

func TestLaunchRequestRejectsInvalidInputBeforeReservation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*LaunchRequest)
	}{
		{"empty body", func(r *LaunchRequest) { r.Body = "" }},
		{"blank body", func(r *LaunchRequest) { r.Body = " \t\n" }},
		{"Claude settings", func(r *LaunchRequest) { r.Harness = "claude"; r.AgentArgs = []string{"--settings", "custom.json"} }},
		{"Codex environment", func(r *LaunchRequest) { r.AgentArgs = []string{"-c", "shell_environment_policy.inherit=all"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			before, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&input)
			if _, err := server.reserveInitialLaunch(ctx, input); err == nil {
				t.Fatal("invalid input reserved")
			}
			after, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatal("invalid input changed task state")
			}
		})
	}
}

func TestLaunchRequestRetryRejectsChangedInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*LaunchRequest)
	}{
		{"harness", func(r *LaunchRequest) { r.Harness = "claude" }},
		{"project", func(r *LaunchRequest) { r.Project = "/different" }},
		{"worktree", func(r *LaunchRequest) { r.Worktree = "/different" }},
		{"branch", func(r *LaunchRequest) { r.Branch = "different" }},
		{"base", func(r *LaunchRequest) { r.BaseCommit = strings.Repeat("b", 40) }},
		{"workspace", func(r *LaunchRequest) { r.Workspace = "different" }},
		{"arguments", func(r *LaunchRequest) { r.AgentArgs = []string{"--model", "different"} }},
		{"body", func(r *LaunchRequest) { r.Body = "Different work" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			before, err := server.reserveInitialLaunch(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			test.change(&input)
			if _, err := server.reserveInitialLaunch(ctx, input); err == nil {
				t.Fatal("changed retry accepted")
			}
			after, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatal("rejected retry changed state")
			}
		})
	}
}

func TestLaunchRequestRejectsBoundAndRetiredTasks(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "bound", true: "retired"}[retired], func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			input.TaskID = "task-1"
			if retired {
				if _, err := server.store.Update(ctx, input.TaskID, func(task taskstate.Task) (taskstate.Task, error) {
					held, err := task.PlaceHold(taskstate.Hold{Reason: "Wait"})
					if err != nil {
						return taskstate.Task{}, err
					}
					finished, err := held.AcceptResult("gen-1", taskstate.Retain, "")
					if err != nil {
						return taskstate.Task{}, err
					}
					return finished.RetireResult("gen-1")
				}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			for _, body := range []string{input.Body, "changed retry"} {
				input.Body = body
				_, err := server.reserveInitialLaunch(ctx, input)
				if retired && !errors.Is(err, ErrLaunchRetired) ||
					!retired && !errors.Is(err, ErrLaunchAlreadyBound) {
					t.Fatalf("inspection conflict: %v", err)
				}
			}
			after, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatal("completed retry changed state")
			}
		})
	}
}

func TestInitialLaunchReservationExcludesTaskOperations(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	release, err := registry.reserveInitialLaunch(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.reserveInitialLaunch(key); !errors.Is(err, errControllerBusy) {
		t.Fatalf("duplicate executor: %v", err)
	}
	if _, err := registry.reserveAdoption(key, controllerEndpoint{session: "work", pane: "pane-1"}); !errors.Is(
		err,
		errControllerBusy,
	) {
		t.Fatalf("concurrent adoption: %v", err)
	}
	other, err := registry.reserveInitialLaunch(controllerKey{kind: taskController, id: "task-2"})
	if err != nil {
		t.Fatalf("unrelated task blocked: %v", err)
	}
	other()
	release()
	release()
	again, err := registry.reserveInitialLaunch(key)
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestInitialLaunchReservationRejectsExistingResident(t *testing.T) {
	server, key, endpoint := taskEndpointFixture(t)
	startEndpointResident(t, server, key, endpoint, func() error { return nil })
	if _, err := server.controllers.reserveInitialLaunch(key); !errors.Is(err, errControllerBusy) {
		t.Fatalf("resident overlaps initial launch: %v", err)
	}
}

func TestManualControlRejectsInitialLaunchEndpoint(t *testing.T) {
	server, _, endpoint := taskEndpointFixture(t)
	queued, err := taskstate.NewQueued(
		taskstate.Definition{ID: "task-2", Title: "Launch", Repo: "repo", Kind: "ship"},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := server.store.Create(ctx, queued); err != nil {
		t.Fatal(err)
	}
	intent := taskstate.LaunchIntent{
		Generation: "gen-2", Harness: "codex", Name: "worker-2", Session: endpoint.session,
		Project: "/repo", Worktree: "/worktree-2", Branch: "task-2", BaseCommit: strings.Repeat("a", 40),
		Workspace: "workspace-1", Label: "concierge-gen-2", Body: "Work",
	}
	if _, err := server.store.ReserveInitialLaunch(ctx, "task-2", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.Update(ctx, "task-2", func(task taskstate.Task) (taskstate.Task, error) { return task.BeginLaunchPreparation("gen-2") }); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.Update(ctx, "task-2", func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchPane("gen-2") }); err != nil {
		t.Fatal(err)
	}
	endpoint.pane = "pane-2"
	if _, err := server.store.RecordLaunchEndpoint(ctx, "task-2", "gen-2", taskstate.LaunchEndpoint{Workspace: "workspace-1", Tab: "tab-2", Pane: endpoint.pane}); err != nil {
		t.Fatal(err)
	}
	if err := server.rejectTaskBoundPane(ctx, endpoint); err == nil {
		t.Fatal("manual control accepted an initial launch endpoint")
	}
}

func TestLaunchRejectsInvalidReservedNameBeforePublication(t *testing.T) {
	for _, name := range []string{"Worker", "concierge-" + strings.Repeat("a", 33)} {
		t.Run(name, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			intent := input.intent(server.config.Session, "generation")
			intent.Name = name
			before, err := server.store.ReserveInitialLaunch(ctx, input.TaskID, intent)
			if err != nil {
				t.Fatal(err)
			}
			published := false
			server.reserveLaunchIntent = func(context.Context, string, taskstate.LaunchIntent) (taskstate.Task, error) {
				published = true
				return before, nil
			}
			if _, err := server.reserveInitialLaunch(ctx, input); err == nil || published {
				t.Fatalf(
					"invalid Herdr name reached publication: name=%q published=%v err=%v",
					name,
					published,
					err,
				)
			}
			after, err := server.store.Read(ctx, input.TaskID)
			if err != nil || !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatalf("changed invalid stored identity: %v", err)
			}
		})
	}
}

func TestHostLaunchRejectsInvalidWorkspaceBeforeReservation(t *testing.T) {
	for _, test := range []struct {
		name, response string
		exitCode       int
	}{
		{"missing workspace", `{"error":{"code":"workspace_not_found","message":"workspace review-rcl not found"}}`, 1},
		{"resolved label", `{"result":{"workspace":{"workspace_id":"w19","label":"review-rcl"}}}`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			git := filepath.Join(t.TempDir(), "git")
			script := "#!/bin/sh\ncase \"$3\" in\ncheck-ref-format) exit 0 ;;\ncat-file) printf 'commit\\n'; exit 0 ;;\nesac\nexit 72\n"
			if err := os.WriteFile(git, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			server.config.GitExecutable = git
			input.Workspace = "review-rcl"
			script = fmt.Sprintf(
				"#!/bin/sh\nprintf '%%s\\n' '%s'\nexit %d\n",
				test.response,
				test.exitCode,
			)
			if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			before, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := server.launch(ctx, input); err == nil {
				t.Fatal("invalid workspace accepted")
			}
			after, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatal("invalid workspace reserved or advanced the task")
			}
		})
	}
}
