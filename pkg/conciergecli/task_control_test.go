package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func TestTaskCancelReleasesOnlyUnboundAdoptions(t *testing.T) {
	for _, binding := range []string{"empty", "pending", "published", "confirmed"} {
		t.Run(binding, func(t *testing.T) {
			root := lifecycleRoot(t)
			store, _ := taskstate.NewStore(root)
			_, err := store.Update(
				context.Background(),
				"task-1",
				func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					switch binding {
					case "empty":
						snapshot.Worker = nil
					case "pending":
						snapshot.Worker.PendingAdoption = true
						snapshot.Worker.Workspace = ""
						snapshot.Worker.Tab = ""
					case "published":
						snapshot.Worker.PendingAdoption = true
					}
					return taskstate.NewMigrated(snapshot)
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "records", "task-1.json")
			before, _ := os.ReadFile(path)
			args := []string{
				"--recipient",
				"../unused",
				"--message-root",
				"relative-unused",
				"task",
				"cancel",
				"task-1",
			}
			if binding == "confirmed" || binding == "published" {
				expectLifecycleError(t, root, 1, args...)
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("changed protected binding")
				}
				return
			}
			for range 2 {
				if got := invoke(t, root, "", args...); got != "task: \"task-1\"\nstate: queued\n" {
					t.Fatalf("cancel: %s", got)
				}
			}
			task, err := store.Read(context.Background(), "task-1")
			if err != nil || task.Snapshot().Worker != nil {
				t.Fatalf("reservation remains: %#v %v", task.Snapshot(), err)
			}
		})
	}
}

func TestTaskStopRequiresGenerationAndPreservesHistoricalWorker(t *testing.T) {
	root := lifecycleRoot(t)
	store, _ := taskstate.NewStore(root)
	_, err := store.Update(
		context.Background(),
		"task-1",
		func(task taskstate.Task) (taskstate.Task, error) {
			snapshot := task.Snapshot()
			snapshot.State = taskstate.InFlight
			snapshot.Worker.Generation = "gen-new"
			snapshot.Receipt = &taskstate.Receipt{Generation: "gen-1", Mode: taskstate.Retain}
			return taskstate.NewMigrated(snapshot)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	record := filepath.Join(directory, "calls")
	if err := os.WriteFile(filepath.Join(directory, "herdr"), []byte("#!/bin/sh\nprintf called >> \"$TEST_CALLS\"\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("TEST_CALLS", record)
	args := []string{
		"--recipient",
		"../unused",
		"--message-root",
		"relative-unused",
		"task",
		"stop",
		"task-1",
		"--generation",
		"gen-1",
	}
	path := filepath.Join(root, "records", "task-1.json")
	before, _ := os.ReadFile(path)
	expectLifecycleError(t, root, 2, args[:len(args)-2]...)
	wrong := append([]string{}, args...)
	wrong[len(wrong)-1] = "gen-wrong"
	expectLifecycleError(t, root, 1, wrong...)
	for range 2 {
		if got := invoke(t, root, "", args...); got != "task: \"task-1\"\ngeneration: \"gen-1\"\n" {
			t.Fatalf("receipt retry: %s", got)
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("changed newer worker")
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("unexpected Herdr calls: %v", err)
	}
	wrong[len(wrong)-1] = "gen-new"
	failed := expectLifecycleError(t, root, 1, wrong...)
	if strings.Contains(failed, "generation:") {
		t.Fatalf("false success: %s", failed)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("current stop skipped Herdr: %v", err)
	}
	if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader(""), failedOutput{}, io.Discard); code != 1 {
		t.Fatalf("stop writer: %d", code)
	}
}

func TestTaskStopUsesRecordedSessionAndPane(t *testing.T) {
	root := lifecycleRoot(t)
	directory := t.TempDir()
	record := filepath.Join(directory, "calls")
	script := `#!/bin/sh
[ "$HERDR_SESSION" = work ] || exit 71
printf '%s\n' "$*" >> "$TEST_CALLS"
case "$*" in
 'session list --json --session work') printf '{"sessions":[{"name":"work","running":true,"socket_path":"%s"}]}\n' "$TEST_SOCKET" ;;
 'pane get pane-1 --session work') printf '%s\n' '{"error":{"code":"pane_not_found"}}' ;;
 *) exit 72 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("HERDR_SESSION", "wrong-ambient")
	t.Setenv("TEST_CALLS", record)
	t.Setenv("TEST_SOCKET", filepath.Join(directory, "herdr.sock"))
	before, _ := os.ReadFile(filepath.Join(root, "records", "task-1.json"))
	got := invoke(t, root, "", "task", "stop", "task-1", "--generation", "gen-1")
	if got != "task: \"task-1\"\ngeneration: \"gen-1\"\n" {
		t.Fatalf("stop: %s", got)
	}
	calls, err := os.ReadFile(record)
	if err != nil ||
		string(
			calls,
		) != "session list --json --session work\nsession list --json --session work\npane get pane-1 --session work\n" {
		t.Fatalf("calls: %s %v", calls, err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "records", "task-1.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("stop changed task record")
	}
}
