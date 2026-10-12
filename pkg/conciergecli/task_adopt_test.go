package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func adoptionCLIFixture(t *testing.T) (string, []string) {
	t.Helper()
	root := taskRoot(t, t.TempDir())
	seedViewTask(t, root, "task-1", taskstate.Queued, nil, nil)
	worktree := t.TempDir()
	project := t.TempDir()
	directory := t.TempDir()
	script := `#!/bin/sh
[ "$HERDR_SESSION" = work ] || exit 71
[ "$TEST_ADOPTION_FAIL" = 1 ] && exit 72
case "$1 $2" in
 'pane get') printf '{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":"%s","workspace_id":"workspace-1","tab_id":"tab-1"}}}\n' "$TEST_WORKTREE" ;;
 'pane process-info') printf '%s\n' '{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}' ;;
 'agent get') printf '%s\n' '{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":"codex"}}}' ;;
 *) exit 73 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("HERDR_SESSION", "wrong-ambient")
	t.Setenv("TEST_WORKTREE", worktree)
	t.Setenv("TEST_PROJECT", project)
	t.Setenv("TEST_ADOPTION_FAIL", "")
	return root, []string{
		"--message-root",
		"relative-unused",
		"--recipient",
		"../unused",
		"task",
		"adopt",
		"task-1",
		"--name",
		"worker",
		"--session",
		"work",
		"--pane",
		"pane-1",
		"--project",
		project,
		"--worktree",
		worktree,
		"--agent-arg",
		"resume",
		"--agent-arg",
		"thread,one",
		"--agent-arg=--flag",
		"--generation",
		"gen-launch",
	}
}

func TestTaskAdoptForwardsBindingAndPreservesGeneration(t *testing.T) {
	root, args := adoptionCLIFixture(t)
	got := invoke(t, root, "", args...)
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	worker := task.Snapshot().Worker
	if worker == nil || worker.PendingAdoption || worker.Workspace != "workspace-1" ||
		worker.Tab != "tab-1" ||
		worker.Name != "worker" ||
		worker.Session != "work" ||
		worker.Harness != "codex" ||
		worker.Generation != "gen-launch" ||
		worker.Pane != "pane-1" ||
		worker.Project != os.Getenv("TEST_PROJECT") ||
		worker.Worktree != os.Getenv("TEST_WORKTREE") ||
		!reflect.DeepEqual(worker.AgentArgs, []string{"resume", "thread,one", "--flag"}) ||
		task.Definition().Kind != "audit" {
		t.Fatalf("binding: %#v", task.Snapshot())
	}
	want := "task: \"task-1\"\nstate: queued\ngeneration: \"" + worker.Generation + "\"\n"
	if got != want {
		t.Fatalf("adoption output: %s", got)
	}
	if got := invoke(t, root, "", args...); got != want {
		t.Fatalf("retry output: %s", got)
	}
	invoke(t, root, "", "backlog", "start", "task-1")
	if got := invoke(t, root, "", args...); got != strings.Replace(
		want,
		"state: queued",
		"state: in_flight",
		1,
	) {
		t.Fatalf("running retry: %s", got)
	}
	conflict := append([]string{}, args...)
	conflict[8] = "another-worker"
	expectLifecycleError(t, root, 1, conflict...)
	after, err := store.Read(context.Background(), "task-1")
	if err != nil || after.Snapshot().Worker.Generation != worker.Generation {
		t.Fatalf("changed generation: %#v %v", after.Snapshot(), err)
	}
}

func TestTaskAdoptRejectsVerificationAndMissingArguments(t *testing.T) {
	root, args := adoptionCLIFixture(t)
	expectLifecycleError(t, root, 2, args[:len(args)-5]...)
	t.Setenv("TEST_ADOPTION_FAIL", "1")
	got := expectLifecycleError(t, root, 1, args...)
	if strings.Contains(got, "generation:") {
		t.Fatalf("false success: %s", got)
	}
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil || task.Snapshot().Worker != nil {
		t.Fatalf("unverified binding: %#v %v", task.Snapshot(), err)
	}
}

func TestTaskAdoptReportsProjectionAndWriterFailures(t *testing.T) {
	root, args := adoptionCLIFixture(t)
	if err := os.Mkdir(filepath.Join(root, "backlog.md"), 0700); err != nil {
		t.Fatal(err)
	}
	failed := expectLifecycleError(t, root, 1, args...)
	if strings.Contains(failed, "generation:") {
		t.Fatalf("false success: %s", failed)
	}
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil || task.Snapshot().Worker == nil || task.Snapshot().Worker.PendingAdoption {
		t.Fatalf("binding lost: %#v %v", task.Snapshot(), err)
	}
	if err := os.Remove(filepath.Join(root, "backlog.md")); err != nil {
		t.Fatal(err)
	}
	if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader(""), failedOutput{}, io.Discard); code != 1 {
		t.Fatalf("writer: %d", code)
	}
	var help bytes.Buffer
	if code := Run(context.Background(), []string{"task", "adopt", "--help"}, strings.NewReader(""), &help, io.Discard); code != 0 ||
		!strings.Contains(help.String(), "--agent-arg") {
		t.Fatalf("help: %d %s", code, &help)
	}
}

func TestTaskAdoptRequiresLaunchGeneration(t *testing.T) {
	root, args := adoptionCLIFixture(t)
	for _, test := range []struct {
		name string
		args []string
		code int
	}{
		{"missing", args[:len(args)-2], 2},
		{"empty", append(append([]string{}, args[:len(args)-1]...), ""), 1},
		{"malformed", append(append([]string{}, args[:len(args)-1]...), "bad generation"), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := expectLifecycleError(t, root, test.code, test.args...)
			if !strings.Contains(got, "generation") {
				t.Fatalf("missing generation error: %s", got)
			}
			store, _ := taskstate.NewStore(root)
			task, err := store.Read(context.Background(), "task-1")
			if err != nil || task.Snapshot().Worker != nil {
				t.Fatalf("invalid adoption changed task: %#v, %v", task.Snapshot(), err)
			}
		})
	}
}
