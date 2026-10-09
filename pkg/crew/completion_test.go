package crew

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
)

type completionRunner struct {
	response string
	session  string
	pane     string
	during   func()
}

func (runner *completionRunner) RunSession(
	_ context.Context, session string, args ...string,
) ([]byte, []byte, error) {
	runner.session = session
	if len(args) >= 3 {
		runner.pane = args[2]
	}
	if runner.during != nil {
		runner.during()
	}
	return []byte(runner.response), nil, nil
}

func TestCompletionRetiresOnlyAfterExactPaneDeparture(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
		wantGone bool
	}{
		{"pane gone", `{"error":{"code":"pane_not_found"}}`, true},
		{"pane present", `{"result":{"pane":{"pane_id":"pane-1"}}}`, false},
		{"agent missing", `{"error":{"code":"agent_not_found"}}`, false},
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
			runner := &completionRunner{response: test.response}
			err = registry.WithCompletion("task-1", func(completion *Completion) error {
				return completion.Retire(
					context.Background(),
					herdr.Client{SessionRunner: runner},
					CloseTask,
				)
			})
			if (err == nil) != test.wantGone {
				t.Fatalf("Retire error = %v, want gone %t", err, test.wantGone)
			}
			for _, path := range []string{
				filepath.Join(root, "tasks", "task-1.meta"),
				filepath.Join(root, "crew", "task-1.json"),
			} {
				_, statErr := os.Lstat(path)
				if errors.Is(statErr, os.ErrNotExist) != test.wantGone {
					t.Fatalf("record %s: %v", path, statErr)
				}
			}
			_, markerErr := os.Lstat(filepath.Join(root, "tasks", "task-1.completion.json"))
			if errors.Is(markerErr, os.ErrNotExist) == test.wantGone {
				t.Fatalf("pending marker: %v", markerErr)
			}
			if runner.session != "work" || runner.pane != "pane-1" {
				t.Fatalf("wrong target: %s %s", runner.session, runner.pane)
			}
		})
	}
}

func TestCompletionResumesAfterRecordRetirement(t *testing.T) {
	root := t.TempDir()
	writeCurrentTask(t, root)
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	runner := &completionRunner{response: `{"error":{"code":"pane_not_found"}}`}
	retire := func(completion *Completion) error {
		return completion.Retire(
			context.Background(),
			herdr.Client{SessionRunner: runner},
			RetainTask,
		)
	}
	if err := registry.WithCompletion("task-1", retire); err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCompletion("task-1", func(completion *Completion) error {
		if !completion.Pending() || completion.Mode() != RetainTask {
			t.Fatalf("lost pending retention: %+v", completion)
		}
		if err := completion.Retire(context.Background(), herdr.Client{}, RetainTask); err != nil {
			return err
		}
		return completion.Clear()
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.completion.json")); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("marker remains: %v", err)
	}
}

func TestPendingCompletionBlocksDispatchAndAdoption(t *testing.T) {
	root := t.TempDir()
	writeCurrentTask(t, root)
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCompletion("task-1", func(completion *Completion) error {
		completion.marker = completionMarker{
			TaskID: "task-1", SpawnGeneration: "gen-1", Session: "work", Pane: "pane-1",
			Mode: CloseTask,
		}
		return completion.publishMarker()
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCurrentTask("task-1", func(*CurrentTask) error { return nil }); !errors.Is(
		err,
		ErrStaleRecord,
	) {
		t.Fatalf("dispatch during completion error = %v", err)
	}
	adoption := Adoption{
		TaskID: "task-1", Session: "work", Pane: "pane-1", Name: "worker-1",
		Project: "/project", Worktree: "/project-worktree", Kind: "crewmate",
		AgentArgs: []string{"resume", "thread-1"},
	}
	if err := registry.AdoptLocalTask(
		context.Background(), adoption, herdr.Client{SessionRunner: &completionRunner{}},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("adoption during completion error = %v", err)
	}
}

func TestCompletionRejectsChangedGenerationOnRetry(t *testing.T) {
	root := t.TempDir()
	writeCurrentTask(t, root)
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCompletion("task-1", func(completion *Completion) error {
		completion.marker = completionMarker{
			TaskID: "task-1", SpawnGeneration: "gen-1", Session: "work", Pane: "pane-1",
			Mode: CloseTask,
		}
		return completion.publishMarker()
	}); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(root, "tasks", "task-1.meta")
	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.Replace(string(metadata), "spawn_gen=gen-1", "spawn_gen=gen-2", 1))
	if err := os.WriteFile(metadataPath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registry.WithCompletion("task-1", func(*Completion) error { return nil }); !errors.Is(
		err,
		ErrStaleRecord,
	) {
		t.Fatalf("changed generation error = %v", err)
	}
}

func TestCompletionResumesInterruptedRecordRemoval(t *testing.T) {
	for _, test := range []struct {
		name       string
		removeMeta bool
		mode       CompletionMode
	}{
		{"close before record removal", false, CloseTask},
		{"close after metadata removal", true, CloseTask},
		{"retain before record removal", false, RetainTask},
		{"retain after metadata removal", true, RetainTask},
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
			if err := registry.WithCompletion("task-1", func(completion *Completion) error {
				completion.marker = completionMarker{
					TaskID: "task-1", SpawnGeneration: "gen-1", Session: "work", Pane: "pane-1",
					Mode: test.mode,
				}
				return completion.publishMarker()
			}); err != nil {
				t.Fatal(err)
			}
			if test.removeMeta {
				if err := os.Remove(filepath.Join(root, "tasks", "task-1.meta")); err != nil {
					t.Fatal(err)
				}
			}
			runner := &completionRunner{response: `{"error":{"code":"pane_not_found"}}`}
			if err := registry.WithCompletion("task-1", func(completion *Completion) error {
				if !completion.Pending() || completion.Mode() != test.mode {
					t.Fatal("pending mode changed")
				}
				otherMode := CloseTask
				if test.mode == CloseTask {
					otherMode = RetainTask
				}
				if err := completion.Retire(
					context.Background(), herdr.Client{SessionRunner: runner}, otherMode,
				); !errors.Is(err, ErrConflict) {
					t.Fatalf("changed mode error = %v", err)
				}
				if err := completion.Clear(); !errors.Is(err, ErrConflict) {
					t.Fatalf("premature clear error = %v", err)
				}
				if _, err := os.Lstat(filepath.Join(root, "crew", "task-1.json")); err != nil {
					t.Fatalf("crew record changed after refusal: %v", err)
				}
				if !test.removeMeta {
					if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.meta")); err != nil {
						t.Fatalf("task metadata changed after refusal: %v", err)
					}
				}
				if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.completion.json")); err != nil {
					t.Fatalf("marker changed after refusal: %v", err)
				}
				return completion.Retire(context.Background(), herdr.Client{SessionRunner: runner}, test.mode)
			}); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{
				filepath.Join(root, "tasks", "task-1.meta"),
				filepath.Join(root, "crew", "task-1.json"),
			} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("record remains at %s: %v", path, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.completion.json")); err != nil {
				t.Fatalf("pending marker lost: %v", err)
			}
		})
	}
}

func TestCompletionRefusesReplacedIdentityDuringPaneLookup(t *testing.T) {
	for _, test := range []struct {
		name    string
		replace func(*testing.T, string)
		paths   []string
	}{
		{"task directory", func(t *testing.T, root string) {
			t.Helper()
			if err := os.Rename(filepath.Join(root, "tasks"), filepath.Join(root, "old-tasks")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "tasks"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{"old-tasks/task-1.meta", "crew/task-1.json"}},
		{"crew directory", func(t *testing.T, root string) {
			t.Helper()
			if err := os.Rename(filepath.Join(root, "crew"), filepath.Join(root, "old-crew")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "crew"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{"tasks/task-1.meta", "old-crew/task-1.json"}},
		{"metadata file", func(t *testing.T, root string) {
			t.Helper()
			path := filepath.Join(root, "tasks", "task-1.meta")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}, []string{"tasks/task-1.meta", "tasks/task-1.meta.old", "crew/task-1.json"}},
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
			runner := &completionRunner{
				response: `{"error":{"code":"pane_not_found"}}`,
				during:   func() { test.replace(t, root) },
			}
			err = registry.WithCompletion("task-1", func(completion *Completion) error {
				return completion.Retire(
					context.Background(),
					herdr.Client{SessionRunner: runner},
					CloseTask,
				)
			})
			if !errors.Is(err, ErrStaleRecord) {
				t.Fatalf("replacement error = %v", err)
			}
			for _, path := range test.paths {
				if _, err := os.Lstat(filepath.Join(root, path)); err != nil {
					t.Fatalf("record changed at %s: %v", path, err)
				}
			}
			for _, name := range []string{"tasks/task-1.completion.json", "old-tasks/task-1.completion.json"} {
				if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
					t.Fatalf("marker published at %s", name)
				}
			}
		})
	}
}
