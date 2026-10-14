package conciergecli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskstate"
)

func launchArguments(root string) []string {
	return []string{
		"--root",
		root,
		"--message-root",
		"/messages",
		"--recipient",
		"concierge",
		"launch",
		"task-1",
		"--harness",
		"codex",
		"--project",
		"/repo",
		"--branch",
		"task/work",
		"--base-commit",
		strings.Repeat("a", 40),
		"--workspace",
		"w1",
		"--agent-arg=--model",
		"--agent-arg=example,model",
		"--body-file",
		"-",
	}
}

func TestLaunchPreservesRequestAndReportsHostOutcome(t *testing.T) {
	for _, mode := range []string{"completed", "owned", "pending", "blocked", "native checkpoint", "supervision stopped", "lost response", "already bound"} {
		t.Run(mode, func(t *testing.T) {
			root := shortTaskRoot(t)
			listener, err := net.ListenUnix(
				"unix",
				&net.UnixAddr{Name: filepath.Join(root, "host.sock"), Net: "unix"},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			received := make(chan hostservice.Request, 1)
			done := make(chan error, 1)
			go func() {
				connection, err := listener.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer connection.Close()
				var request hostservice.Request
				if err := json.NewDecoder(connection).Decode(&request); err != nil {
					done <- err
					return
				}
				received <- request
				if mode == "lost response" {
					done <- nil
					return
				}
				response := hostservice.Response{
					Outcome: hostservice.Failed,
					Error:   hostservice.ErrLaunchAlreadyBound.Error(),
				}
				if mode == "completed" || mode == "owned" || mode == "supervision stopped" {
					response = hostservice.Response{
						Outcome: hostservice.Completed,
						Transition: &taskstate.Snapshot{
							Definition: taskstate.Definition{
								ID:    "task-1",
								Title: "Work",
								Repo:  "repo",
								Kind:  "ship",
							}, State: taskstate.InFlight,
							Worker: &taskstate.Worker{
								Name:       "worker",
								Harness:    "codex",
								Session:    "work",
								Workspace:  "w1",
								Tab:        "w1:t1",
								Pane:       "w1:p1",
								Project:    "/repo",
								Worktree:   "/worker",
								Generation: "gen-1",
								AgentArgs:  []string{"resume", "native-session"},
							},
						},
					}
				}
				if response.Transition != nil && response.Transition.Worker != nil {
					response.Transition.Worker.Lease = taskstate.WorktreeLease{
						ID:         "lease-1",
						Holder:     "holder",
						Project:    "/repo",
						Path:       "/worker",
						Branch:     "task/work",
						BaseCommit: strings.Repeat("a", 40),
						State:      taskstate.LeaseHeld,
					}
				}
				if mode == "owned" {
					response.Transition.Worker.OwnedWorkspaceLabel = "concierge-gen-1"
				}
				if mode == "supervision stopped" {
					response.Outcome = hostservice.Blocked
					response.Error = "supervision registration stopped"
				}
				if mode == "pending" || mode == "blocked" || mode == "native checkpoint" {
					input := request.Launch
					task, err := taskstate.NewQueued(
						taskstate.Definition{
							ID:    input.TaskID,
							Title: "Work",
							Repo:  "repo",
							Kind:  "task",
						},
					)
					if err != nil {
						done <- err
						return
					}
					task, err = task.ReserveInitialLaunch(
						taskstate.LaunchIntent{
							Generation: "gen-1",
							Name:       "worker",
							Label:      "launch-one",
							Harness:    input.Harness,
							Session:    "work",
							Project:    input.Project,
							Worktree:   input.Worktree,
							Branch:     input.Branch,
							BaseCommit: input.BaseCommit,
							Workspace:  input.Workspace,
							AgentArgs:  input.AgentArgs,
							Body:       input.Body,
						},
					)
					if err != nil {
						done <- err
						return
					}
					snapshot := task.Snapshot()
					response = hostservice.Response{
						Outcome:    hostservice.Outcome(mode),
						Error:      "native startup awaits inspection",
						Transition: &snapshot,
					}
				}
				if mode == "native checkpoint" {
					response.Outcome = hostservice.Blocked
					response.Transition.LaunchIntent.Worktree = "/worker"
					response.Transition.LaunchIntent.LeaseHolder = "holder"
					response.Transition.LaunchIntent.Lease = taskstate.WorktreeLease{
						ID:         "lease-1",
						Holder:     "holder",
						Project:    "/repo",
						Path:       "/worker",
						Branch:     "task/work",
						BaseCommit: strings.Repeat("a", 40),
						State:      taskstate.LeaseHeld,
					}
					response.Transition.LaunchIntent.Phase = taskstate.LaunchAgentRequested
					response.Transition.LaunchIntent.Endpoint = &taskstate.LaunchEndpoint{
						Workspace: request.Launch.Workspace,
						Tab:       "t1",
						Pane:      "w1:p1",
					}
					response.Checkpoint = &hostservice.NativeCheckpoint{
						Kind:         "native_input",
						Notification: "sent",
					}
				}
				done <- json.NewEncoder(connection).Encode(response)
			}()
			body := " \nDo the requested work.\n "
			var output bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			arguments := launchArguments(root)
			if mode == "owned" {
				for index, arg := range arguments {
					if arg == "--workspace" {
						arguments = append(arguments[:index], arguments[index+2:]...)
						break
					}
				}
			}
			code := Run(ctx, arguments, strings.NewReader(body), &output, io.Discard)
			if mode == "completed" || mode == "owned" {
				if code != 0 || !strings.Contains(output.String(), "generation: \"gen-1\"") ||
					!strings.Contains(output.String(), "pane: \"w1:p1\"") {
					t.Fatalf("launch code %d: %s", code, &output)
				}
			} else if mode == "supervision stopped" {
				if code != 1 || !strings.Contains(output.String(), "tracking: stopped") || !strings.Contains(output.String(), "phase: supervision_stopped") || !strings.Contains(output.String(), "concierge supervise task") {
					t.Fatalf("stopped supervision code %d: %s", code, &output)
				}
			} else if mode == "native checkpoint" {
				if code != 1 {
					t.Fatalf("checkpoint code %d: %s", code, &output)
				}
				for _, part := range []string{"launch: blocked", "tracking: automatic", "checkpoint: native_input", "notification: sent", "session: \"work\"", "pane: \"w1:p1\"", "worktree: \"/worker\"", "operator", "native dialog"} {
					if !strings.Contains(output.String(), part) {
						t.Fatalf("checkpoint output lacks %q: %s", part, &output)
					}
				}
			} else if mode == "pending" || mode == "blocked" {
				wantCode := 0
				if mode == "blocked" {
					wantCode = 1
				}
				if code != wantCode || !strings.Contains(output.String(), "launch: "+mode) || !strings.Contains(output.String(), "concierge worker show") || !strings.Contains(output.String(), "tracking: automatic") {
					t.Fatalf("tracked launch code %d: %s", code, &output)
				}
			} else if code != 1 || !strings.Contains(output.String(), "inspect") {
				t.Fatalf("failed launch code %d: %s", code, &output)
			}
			if mode == "lost response" &&
				!strings.Contains(output.String(), "host operation outcome is unknown") {
				t.Fatalf("lost unknown-outcome error: %s", &output)
			}
			select {
			case request := <-received:
				expected := &hostservice.LaunchRequest{
					TaskID:     "task-1",
					Harness:    "codex",
					Project:    "/repo",
					Worktree:   "",
					Branch:     "task/work",
					BaseCommit: strings.Repeat("a", 40),
					Workspace:  "w1",
					AgentArgs:  []string{"--model", "example,model"},
					Body:       body,
				}
				if mode == "owned" {
					expected.Workspace = ""
				}
				if request.Operation != hostservice.Launch ||
					!reflect.DeepEqual(request.Launch, expected) {
					t.Fatalf("request: %+v", request)
				}
				if request.Expectations == nil || request.Expectations.MessageRoot != "/messages" ||
					request.Expectations.Recipient != "concierge" {
					t.Fatalf("host expectations: %+v", request.Expectations)
				}
			case <-ctx.Done():
				t.Fatal("launch did not send the host request")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			store, _ := taskstate.NewStore(root)
			tasks, err := store.List(context.Background())
			if err != nil || len(tasks) != 0 {
				t.Fatal("CLI changed local task state")
			}
		})
	}
}

func TestLaunchWithoutHostDoesNotCreateTaskState(t *testing.T) {
	root := shortTaskRoot(t)
	var output bytes.Buffer
	code := Run(
		context.Background(),
		launchArguments(root),
		strings.NewReader("Work"),
		&output,
		io.Discard,
	)
	if code != 1 || !strings.Contains(output.String(), "host service unavailable") ||
		!strings.Contains(output.String(), "inspect") {
		t.Fatalf("missing host code %d: %s", code, &output)
	}
	store, _ := taskstate.NewStore(root)
	tasks, err := store.List(context.Background())
	if err != nil || len(tasks) != 0 {
		t.Fatal("missing host caused local mutation")
	}
}
