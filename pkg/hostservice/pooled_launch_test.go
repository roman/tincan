package hostservice

import (
	"context"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/worktree"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFreshLaunchRequiresPoolAdmissionBeforeReservation(t *testing.T) {
	for _, condition := range []string{"missing pool", "caller worktree"} {
		t.Run(condition, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			ctx := context.Background()
			git := filepath.Join(t.TempDir(), "git")
			if err := os.WriteFile(git, []byte("#!/bin/sh\ncase \"$3\" in\ncheck-ref-format) exit 0 ;;\ncat-file) printf 'commit\\n'; exit 0 ;;\nesac\nexit 72\n"), 0700); err != nil {
				t.Fatal(err)
			}
			server.config.GitExecutable = git
			if condition == "missing pool" {
				input.Worktree = ""
			}
			before, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = server.executeInitialLaunch(ctx, input, operatorLaunch)
			expected := "fresh launches use a treehouse lease"
			if condition == "missing pool" {
				expected = "absolute treehouse executable"
			}
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("pool admission: %v", err)
			}
			after, err := server.store.Read(ctx, input.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatal("pool admission failure reserved a launch")
			}
		})
	}
}

func TestHostPooledLaunchRecoversWithTheSameLease(t *testing.T) {
	executable := os.Getenv("TREEHOUSE_TEST_EXECUTABLE")
	if executable == "" {
		t.Skip("set TREEHOUSE_TEST_EXECUTABLE to the pinned treehouse binary")
	}
	for _, mode := range []string{"complete", "lost workspace response"} {
		t.Run(mode, func(t *testing.T) {
			server, input := launchRequestFixture(t)
			server.config.Diagnostics = io.Discard
			input.Worktree, input.Workspace = "", ""
			input.Project = t.TempDir()
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			git, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) string {
				t.Helper()
				output, err := exec.Command(git, append([]string{"-C", input.Project}, args...)...).
					CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %s: %v", args, output, err)
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
			if err := os.WriteFile(filepath.Join(input.Project, "treehouse.toml"), []byte("max_trees = 2\nroot = \"./\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			server.config.GitExecutable = git
			server.config.TreehouseExecutable = executable
			state := t.TempDir()
			t.Setenv("HOST_POOL_PROBE", state)
			t.Setenv("HOST_POOL_MODE", mode)
			script := `#!/bin/sh
case "$1 $2" in
'workspace create')
 shift 2
 while [ "$#" -gt 0 ]; do
  case "$1" in --cwd) printf '%s' "$2" > "$HOST_POOL_PROBE/cwd"; shift ;; --label) printf '%s' "$2" > "$HOST_POOL_PROBE/label"; shift ;; esac
  shift
 done
 printf 'create\n' >> "$HOST_POOL_PROBE/creates"
 [ "$HOST_POOL_MODE" != 'lost workspace response' ] || exit 72
 printf '{"result":{"workspace":{"workspace_id":"w1","label":"%s"},"tab":{"workspace_id":"w1","tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}}' "$(cat "$HOST_POOL_PROBE/label")"
 ;;
'workspace list') printf '{"result":{"workspaces":[{"workspace_id":"w1","label":"%s"}]}}' "$(cat "$HOST_POOL_PROBE/label")" ;;
'pane list') printf '%s' '{"result":{"panes":[{"workspace_id":"w1","tab_id":"w1:t1","pane_id":"w1:p1"}]}}' ;;
'pane get') printf '{"result":{"pane":{"workspace_id":"w1","tab_id":"w1:t1","pane_id":"w1:p1","foreground_cwd":"%s"}}}' "$(cat "$HOST_POOL_PROBE/cwd")" ;;
'pane process-info') printf '%s' '{"result":{"type":"pane_process_info","process_info":{"pane_id":"w1:p1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}' ;;
'agent get')
 if [ ! -f "$HOST_POOL_PROBE/started" ]; then printf '%s' '{"error":{"code":"agent_not_found"}}' >&2; exit 1; fi
 printf '%s' '{"result":{"agent":{"pane_id":"w1:p1","agent":"codex","agent_status":"idle","agent_session":{"source":"herdr:codex","agent":"codex","kind":"id","value":"native-session"}}}}'
 ;;
'agent start') printf 'start\n' >> "$HOST_POOL_PROBE/started"; printf '%s' '{"result":{}}' ;;
'agent prompt') printf '%s' '{"result":{}}' ;;
*) exit 73 ;;
esac
`
			if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			result, err := server.executeInitialLaunch(ctx, input, operatorLaunch)
			var original taskstate.WorktreeLease
			if mode == "lost workspace response" {
				if err == nil {
					t.Fatal("lost response succeeded")
				}
				task, readErr := server.store.Read(ctx, input.TaskID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				original = task.Snapshot().LaunchIntent.Lease
				reconstructed, constructErr := New(server.config)
				if constructErr != nil {
					t.Fatal(constructErr)
				}
				reconstructed.controllers = newControllerRegistry(ctx)
				defer reconstructed.controllers.close()
				if err := reconstructed.controllers.awaitInitialResident(controllerKey{kind: taskController, id: input.TaskID}); err != nil {
					t.Fatal(err)
				}
				if err := reconstructed.reconcileInitialTask(ctx, controllerKey{kind: taskController, id: input.TaskID}); err != nil {
					t.Fatal(err)
				}
				defer func() {
					_, _ = reconstructed.supervision(
						ctx,
						StopSupervision,
						SupervisionRequest{TaskID: input.TaskID},
					)
				}()
				task, readErr = reconstructed.store.Read(ctx, input.TaskID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				snapshot := task.Snapshot()
				result, err = Response{Transition: &snapshot}, nil
			}
			if err != nil {
				t.Fatal(err)
			}
			worker := result.Transition.Worker
			if worker == nil || worker.Lease.ID == "" ||
				worker.Lease.State != taskstate.LeaseHeld ||
				worker.Worktree != worker.Lease.Path ||
				worker.Lease.BaseCommit != input.BaseCommit {
				t.Fatalf("pooled worker: %+v", worker)
			}
			if mode == "lost workspace response" && worker.Lease != original {
				t.Fatalf("replaced recovered lease: %+v", worker.Lease)
			}
			if err := (worktree.Client{Executable: git}).VerifyIdentity(ctx, worktree.Specification{Project: input.Project, Path: worker.Worktree, Branch: input.Branch, BaseCommit: input.BaseCommit}); err != nil {
				t.Fatal(err)
			}
			if err := validateLaunchResponse(result, Request{Operation: Launch, Launch: &input}); err != nil {
				t.Fatal(err)
			}
			for name, expected := range map[string]string{"creates": "create\n", "started": "start\n"} {
				data, err := os.ReadFile(filepath.Join(state, name))
				if err != nil || string(data) != expected {
					t.Fatalf("%s effects: %q; %v", name, data, err)
				}
			}
		})
	}
}
