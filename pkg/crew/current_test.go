package crew

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
)

func writeCurrentTask(t *testing.T, root string) {
	t.Helper()
	state := filepath.Join(root, "tasks")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	data := strings.Join([]string{
		"window=work:pane-1", "endpoint_task_id=task-1", "worktree=/project-worktree",
		"project=/project", "harness=codex", "kind=crewmate", "spawn_gen=gen-1",
		"backend=herdr", "herdr_session=work", "herdr_workspace_id=workspace-1",
		"herdr_tab_id=tab-1", "herdr_pane_id=pane-1", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPendingLaunchBlocksOrdinaryTaskAccess(t *testing.T) {
	root := t.TempDir()
	writeCurrentTask(t, root)
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	if err := registry.WithLaunchTask("task-1", func(task *CurrentTask) error {
		_, err := task.ReserveLaunch("gen-2")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCurrentTask("task-1", func(*CurrentTask) error {
		t.Fatal("ordinary access reached a reserved launch")
		return nil
	}); !errors.Is(err, ErrStaleRecord) {
		t.Fatalf("ordinary access = %v", err)
	}
	if err := registry.WithLaunchTask("task-1", func(task *CurrentTask) error {
		if task.Record().PendingLaunch != "gen-2" {
			t.Fatalf("record = %+v", task.Record())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWithCurrentTaskHoldsMetadataLockDuringOperation(t *testing.T) {
	root := t.TempDir()
	writeCurrentTask(t, root)
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	want := errors.New("operation failed")
	err = registry.WithCurrentTask("task-1", func(task *CurrentTask) error {
		record := task.Record()
		if record.TaskID != "task-1" {
			t.Fatalf("record = %+v", record)
		}
		state, _ := firstmate.ParseAbsolutePath(filepath.Join(root, "tasks"))
		id, _ := firstmate.ParseTaskID("task-1")
		if _, lockErr := firstmate.AcquireTaskLock(state, id, 50*time.Millisecond); !errors.Is(
			lockErr,
			firstmate.ErrLockTimeout,
		) {
			t.Fatalf("metadata lock was not held: %v", lockErr)
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("operation error = %v", err)
	}
	state, _ := firstmate.ParseAbsolutePath(filepath.Join(root, "tasks"))
	id, _ := firstmate.ParseTaskID("task-1")
	lock, err := firstmate.AcquireTaskLock(state, id, time.Second)
	if err != nil {
		t.Fatalf("metadata lock remained held: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentTaskCheckRejectsReplacedCrewRecord(t *testing.T) {
	root := t.TempDir()
	writeCurrentTask(t, root)
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	err = registry.WithCurrentTask("task-1", func(task *CurrentTask) error {
		if err := task.CheckCurrent(); err != nil {
			t.Fatal(err)
		}
		replacement := testRecord()
		replacement.Name = "replacement-worker"
		data, err := json.Marshal(replacement)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "crew", "task-1.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return task.CheckCurrent()
	})
	if !errors.Is(err, ErrStaleRecord) {
		t.Fatalf("replacement check error = %v", err)
	}
}

func TestWithCurrentTaskRejectsStaleBinding(t *testing.T) {
	for _, test := range []struct {
		name         string
		replacements []string
	}{
		{name: "generation", replacements: []string{"spawn_gen=gen-1", "spawn_gen=gen-2"}},
		{name: "pane", replacements: []string{"pane-1", "pane-2"}},
		{name: "worktree", replacements: []string{"worktree=/project-worktree", "worktree=/other"}},
		{name: "project", replacements: []string{"project=/project", "project=/other"}},
		{name: "harness", replacements: []string{"harness=codex", "harness=claude"}},
		{name: "session", replacements: []string{"window=work:pane-1", "window=other:pane-1", "herdr_session=work", "herdr_session=other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeCurrentTask(t, root)
			registry, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(testRecord()); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(root, "tasks")
			data, err := os.ReadFile(filepath.Join(state, "task-1.meta"))
			if err != nil {
				t.Fatal(err)
			}
			updatedData := strings.NewReplacer(test.replacements...).Replace(string(data))
			if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(updatedData), 0o600); err != nil {
				t.Fatal(err)
			}
			called := false
			err = registry.WithCurrentTask("task-1", func(*CurrentTask) error {
				called = true
				return nil
			})
			if !errors.Is(err, ErrStaleRecord) || called {
				t.Fatalf("error = %v, operation called = %v", err, called)
			}
		})
	}
}

func TestSelectedCrewDirectoryKeepsOriginalRecord(t *testing.T) {
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	registryRoot, err := verifiedRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer registryRoot.Close()
	selected, _, err := openSelectedDirectory(registryRoot, "crew")
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if err := os.Rename(filepath.Join(root, "crew"), filepath.Join(root, "old-crew")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "crew"), 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := testRecord()
	replacement.AgentArgs = []string{"resume", "other-thread"}
	data, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "crew", "task-1.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readRecord(selected, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.AgentArgs, testRecord().AgentArgs) {
		t.Fatalf("selected replacement launch arguments: %+v", got.AgentArgs)
	}
}
