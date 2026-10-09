package crew

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func registeredTask(t *testing.T) (string, *Registry) {
	t.Helper()
	root := t.TempDir()
	writeCurrentTask(t, root)
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	return root, registry
}

func TestCurrentTaskAdvancesGeneration(t *testing.T) {
	root, registry := registeredTask(t)
	err := registry.WithCurrentTask("task-1", func(task *CurrentTask) error {
		return task.AdvanceGeneration("gen-2")
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := registry.Get("task-1")
	if err != nil || record.SpawnGeneration != "gen-2" || record.PendingGeneration != "" {
		t.Fatalf("record = %+v, error = %v", record, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "tasks", "task-1.meta"))
	if err != nil || !strings.Contains(string(data), "spawn_gen=gen-2\n") {
		t.Fatalf("metadata = %q, error = %v", data, err)
	}
	if err := registry.WithCurrentTask("task-1", func(task *CurrentTask) error {
		if task.Record().SpawnGeneration != "gen-2" {
			t.Fatalf("record after rotation = %+v", task.Record())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentTaskRecoversPendingGeneration(t *testing.T) {
	for _, metadataGeneration := range []string{"gen-1", "gen-2"} {
		t.Run(metadataGeneration, func(t *testing.T) {
			root, registry := registeredTask(t)
			record := testRecord()
			record.PendingGeneration = "gen-2"
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "crew", "task-1.json"), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			metadataPath := filepath.Join(root, "tasks", "task-1.meta")
			if metadataGeneration == "gen-2" {
				data, err := os.ReadFile(metadataPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(metadataPath, []byte(strings.Replace(string(data), "spawn_gen=gen-1", "spawn_gen=gen-2", 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := registry.WithCurrentTask("task-1", func(task *CurrentTask) error {
				if task.Record().SpawnGeneration != "gen-2" || task.Record().PendingGeneration != "" {
					t.Fatalf("unrecovered record = %+v", task.Record())
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			final, err := registry.Get("task-1")
			if err != nil || final.SpawnGeneration != "gen-2" || final.PendingGeneration != "" {
				t.Fatalf("final record = %+v, error = %v", final, err)
			}
		})
	}
}

func TestCurrentTaskRefusesUnrelatedGenerationDuringRecovery(t *testing.T) {
	root, registry := registeredTask(t)
	record := testRecord()
	record.PendingGeneration = "gen-2"
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "crew", "task-1.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(root, "tasks", "task-1.meta")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, []byte(strings.Replace(string(data), "spawn_gen=gen-1", "spawn_gen=gen-3", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = registry.WithCurrentTask("task-1", func(*CurrentTask) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrStaleRecord) || called {
		t.Fatalf("error = %v, called = %v", err, called)
	}
}

func TestCurrentTaskRefusesChangedIdentityDuringRecovery(t *testing.T) {
	for _, test := range []struct {
		name         string
		replacements []string
	}{
		{name: "pane", replacements: []string{"pane-1", "pane-2"}},
		{name: "worktree", replacements: []string{"worktree=/project-worktree", "worktree=/other"}},
		{name: "project", replacements: []string{"project=/project", "project=/other"}},
		{name: "harness", replacements: []string{"harness=codex", "harness=claude"}},
		{name: "session", replacements: []string{"window=work:pane-1", "window=other:pane-1", "herdr_session=work", "herdr_session=other"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, registry := registeredTask(t)
			record := testRecord()
			record.PendingGeneration = "gen-2"
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "crew", "task-1.json"), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			metadataPath := filepath.Join(root, "tasks", "task-1.meta")
			data, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.NewReplacer(test.replacements...).Replace(string(data))
			if err := os.WriteFile(metadataPath, []byte(changed), 0o600); err != nil {
				t.Fatal(err)
			}
			called := false
			err = registry.WithCurrentTask("task-1", func(*CurrentTask) error {
				called = true
				return nil
			})
			if !errors.Is(err, ErrStaleRecord) || called {
				t.Fatalf("error = %v, callback called = %v", err, called)
			}
			finalData, err := os.ReadFile(metadataPath)
			if err != nil || !strings.Contains(string(finalData), "spawn_gen=gen-1\n") {
				t.Fatalf("metadata changed during refused recovery: %q, error %v", finalData, err)
			}
		})
	}
}
