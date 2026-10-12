package taskstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
)

func TestInventoryLegacyJoinsWorkerAndRetirement(t *testing.T) {
	root := legacyInventoryRoot(t)
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-queued - Waiting (repo: exmachina) (kind: ship)\n## In flight\n- **task-active** - Running (repo: exmachina) (kind: ship)\n## Done\n- [x] task-retired - Cleaning (repo: exmachina) (kind: ship)\n",
	)
	writeLegacyJSON(t, root, "crew/task-active.json", legacyCrewRecord("task-active", "gen-2"))
	writeLegacyFile(t, root, "tasks/task-active.meta", legacyTaskMetadata("task-active", "gen-2"))
	writeLegacyJSON(t, root, "tasks/task-active.receipt.json", map[string]string{
		"task_id": "task-active", "spawn_generation": "gen-1", "mode": "retain",
	})
	writeLegacyJSON(t, root, "tasks/task-retired.completion.json", map[string]string{
		"task_id": "task-retired", "spawn_generation": "gen-3", "session": "work",
		"pane": "pane-1", "mode": "close",
	})
	selected, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	tasks, err := inventoryLegacy(selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("task count = %d, want 3", len(tasks))
	}
	byID := make(map[string]Snapshot, len(tasks))
	for _, task := range tasks {
		byID[task.ID()] = task.Snapshot()
	}
	if got := byID["task-active"]; got.Worker == nil || got.Worker.Generation != "gen-2" ||
		got.Receipt == nil || got.Receipt.Generation != "gen-1" {
		t.Fatalf("active task = %#v", got)
	}
	if got := byID["task-retired"]; got.Worker != nil || got.Completion == nil ||
		got.Completion.Phase != Retired || got.Completion.Generation != "gen-3" {
		t.Fatalf("retired task = %#v", got)
	}
	if got := byID["task-queued"]; got.Worker != nil || got.Completion != nil {
		t.Fatalf("queued task = %#v", got)
	}
}

func TestInventoryLegacyRejectsInconsistentWorker(t *testing.T) {
	tests := []struct {
		name    string
		backlog string
		setup   func(*testing.T, string)
	}{
		{name: "orphan crew", setup: func(t *testing.T, root string) {
			writeLegacyJSON(t, root, "crew/other.json", legacyCrewRecord("other", "gen-1"))
		}},
		{name: "missing metadata", setup: func(t *testing.T, root string) {
			writeLegacyJSON(t, root, "crew/task-1.json", legacyCrewRecord("task-1", "gen-1"))
		}},
		{name: "generation mismatch", setup: func(t *testing.T, root string) {
			writeLegacyJSON(t, root, "crew/task-1.json", legacyCrewRecord("task-1", "gen-1"))
			writeLegacyFile(t, root, "tasks/task-1.meta", legacyTaskMetadata("task-1", "gen-2"))
		}},
		{name: "orphan metadata", setup: func(t *testing.T, root string) {
			writeLegacyFile(t, root, "tasks/task-1.meta", legacyTaskMetadata("task-1", "gen-1"))
		}},
		{name: "unknown crew field", setup: func(t *testing.T, root string) {
			data, err := json.Marshal(legacyCrewRecord("task-1", "gen-1"))
			if err != nil {
				t.Fatal(err)
			}
			writeLegacyFile(
				t,
				root,
				"crew/task-1.json",
				string(data[:len(data)-1])+`,"extra":"lost"}`,
			)
			writeLegacyFile(t, root, "tasks/task-1.meta", legacyTaskMetadata("task-1", "gen-1"))
		}},
		{name: "duplicate crew field", setup: func(t *testing.T, root string) {
			data, err := json.Marshal(legacyCrewRecord("task-1", "gen-1"))
			if err != nil {
				t.Fatal(err)
			}
			writeLegacyFile(
				t,
				root,
				"crew/task-1.json",
				string(data[:len(data)-1])+`,"task_id":"task-1"}`,
			)
			writeLegacyFile(t, root, "tasks/task-1.meta", legacyTaskMetadata("task-1", "gen-1"))
		}},
		{
			name:    "invalid completion endpoint",
			backlog: "# Tasks\n## Done\n- [x] task-1 - Finished (repo: exmachina) (kind: ship)\n",
			setup: func(t *testing.T, root string) {
				writeLegacyJSON(t, root, "tasks/task-1.completion.json", map[string]string{
					"task_id": "task-1", "spawn_generation": "gen-1", "session": "bad/name",
					"pane": "pane-1", "mode": "close",
				})
			},
		},
		{name: "retired before backlog transition", setup: func(t *testing.T, root string) {
			writeLegacyJSON(t, root, "tasks/task-1.completion.json", map[string]string{
				"task_id": "task-1", "spawn_generation": "gen-1", "session": "work",
				"pane": "pane-1", "mode": "close",
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := legacyInventoryRoot(t)
			backlog := test.backlog
			if backlog == "" {
				backlog = "# Tasks\n## In flight\n- **task-1** - Running (repo: exmachina) (kind: ship)\n"
			}
			writeLegacyFile(
				t,
				root,
				"backlog.md",
				backlog,
			)
			test.setup(t, root)
			selected, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer selected.Close()
			if _, err := inventoryLegacy(selected); err == nil {
				t.Fatal("inventory accepted inconsistent legacy state")
			}
		})
	}
}

func TestInventoryLegacyPreservesPendingWorkerTransitions(t *testing.T) {
	root := legacyInventoryRoot(t)
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Queued\n- [ ] task-adopting - Waiting (repo: exmachina) (kind: ship)\n## In flight\n- **task-rotating** - Running (repo: exmachina) (kind: ship)\n",
	)
	adopting := legacyCrewRecord("task-adopting", "gen-1")
	adopting.PendingAdoption = true
	writeLegacyJSON(t, root, "crew/task-adopting.json", adopting)
	rotating := legacyCrewRecord("task-rotating", "gen-1")
	rotating.Name, rotating.PaneID = "worker-2", "pane-2"
	rotating.PendingGeneration = "gen-2"
	rotating.PendingLaunch = "gen-2"
	writeLegacyJSON(t, root, "crew/task-rotating.json", rotating)
	writeLegacyFile(
		t,
		root,
		"tasks/task-rotating.meta",
		strings.ReplaceAll(legacyTaskMetadata("task-rotating", "gen-2"), "pane-1", "pane-2"),
	)
	selected, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	tasks, err := inventoryLegacy(selected)
	if err != nil {
		t.Fatal(err)
	}
	if got := tasks[0].Snapshot().Worker; got == nil || !got.PendingAdoption ||
		got.Workspace != "" {
		t.Fatalf("pending adoption = %#v", got)
	}
	if got := tasks[1].Snapshot().Worker; got == nil || got.PendingGeneration != "gen-2" ||
		got.PendingLaunch != "gen-2" || got.Generation != "gen-1" {
		t.Fatalf("pending generation = %#v", got)
	}
}

func TestInventoryLegacyPreservesInterruptedCleanup(t *testing.T) {
	root := legacyInventoryRoot(t)
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## Done\n- [x] task-1 - Finished (repo: exmachina) (kind: ship)\n",
	)
	writeLegacyJSON(t, root, "crew/task-1.json", legacyCrewRecord("task-1", "gen-1"))
	writeLegacyJSON(t, root, "tasks/task-1.completion.json", map[string]string{
		"task_id": "task-1", "spawn_generation": "gen-1", "session": "work",
		"pane": "pane-1", "mode": "close",
	})
	selected, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	tasks, err := inventoryLegacy(selected)
	if err != nil {
		t.Fatal(err)
	}
	got := tasks[0].Snapshot()
	if got.Worker == nil || got.Worker.AgentArgs[0] != "resume" || got.Worker.Workspace != "" ||
		got.Completion == nil || got.Completion.Phase != Retired {
		t.Fatalf("interrupted cleanup = %#v", got)
	}
}

func TestInventoryLegacyPreservesAcceptedResult(t *testing.T) {
	root := legacyInventoryRoot(t)
	writeLegacyFile(
		t,
		root,
		"backlog.md",
		"# Tasks\n## In flight\n- **task-1** - Running (repo: exmachina) (kind: ship)\n",
	)
	writeLegacyJSON(t, root, "crew/task-1.json", legacyCrewRecord("task-1", "gen-1"))
	writeLegacyFile(t, root, "tasks/task-1.meta", legacyTaskMetadata("task-1", "gen-1"))
	writeLegacyJSON(t, root, "tasks/task-1.completion.json", map[string]string{
		"task_id": "task-1", "spawn_generation": "gen-1", "session": "work",
		"pane": "pane-1", "mode": "close",
	})
	selected, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	tasks, err := inventoryLegacy(selected)
	if err != nil {
		t.Fatal(err)
	}
	got := tasks[0].Snapshot()
	if got.Worker == nil || got.Completion == nil || got.Completion.Phase != Accepted {
		t.Fatalf("accepted result = %#v", got)
	}
}

func TestInventoryLegacyRejectsDuplicateWorkerIdentity(t *testing.T) {
	tests := []struct {
		name      string
		otherName string
		otherPane string
	}{
		{name: "name", otherName: "worker-1", otherPane: "pane-2"},
		{name: "pane", otherName: "worker-2", otherPane: "pane-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := legacyInventoryRoot(t)
			writeLegacyFile(
				t,
				root,
				"backlog.md",
				"# Tasks\n## In flight\n- **task-1** - First (repo: exmachina) (kind: ship)\n- **task-2** - Second (repo: exmachina) (kind: ship)\n",
			)
			writeLegacyJSON(t, root, "crew/task-1.json", legacyCrewRecord("task-1", "gen-1"))
			writeLegacyFile(t, root, "tasks/task-1.meta", legacyTaskMetadata("task-1", "gen-1"))
			other := legacyCrewRecord("task-2", "gen-2")
			other.Name, other.PaneID = test.otherName, test.otherPane
			writeLegacyJSON(t, root, "crew/task-2.json", other)
			writeLegacyFile(t, root, "tasks/task-2.meta", strings.ReplaceAll(
				legacyTaskMetadata("task-2", "gen-2"), "pane-1", test.otherPane,
			))
			selected, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer selected.Close()
			if _, err := inventoryLegacy(selected); err == nil {
				t.Fatal("inventory accepted duplicate worker identity")
			}
		})
	}
}

func legacyInventoryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"tasks", "crew"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeLegacyFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyJSON(t *testing.T, root, name string, content any) {
	t.Helper()
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	writeLegacyFile(t, root, name, string(data))
}

func legacyCrewRecord(taskID, generation string) crew.Record {
	return crew.Record{
		TaskID: taskID, Name: "worker-1", Kind: "codex", PaneID: "pane-1",
		HerdrSession: "work", Project: "/project", Worktree: "/worktree",
		SpawnGeneration: generation, AgentArgs: []string{"resume", "thread-1"},
	}
}

func legacyTaskMetadata(taskID, generation string) string {
	return "window=work:pane-1\nendpoint_task_id=" + taskID +
		"\nworktree=/worktree\nproject=/project\nharness=codex\nkind=ship\nspawn_gen=" +
		generation + "\nbackend=herdr\nherdr_session=work\nherdr_workspace_id=workspace-1\n" +
		"herdr_tab_id=tab-1\nherdr_pane_id=pane-1\n"
}
