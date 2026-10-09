package firstmate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validMetadata = "window=crew:pane-1\nendpoint_task_id=task-1\nworktree=/repo/worktree\nproject=/repo\nharness=codex\nkind=ship\nspawn_gen=s123.456.7\nbackend=herdr\nherdr_session=crew\nherdr_workspace_id=workspace-1\nherdr_tab_id=tab:1\nherdr_pane_id=pane-1\n"

func TestReadTaskFromRootKeepsSelectedDirectory(t *testing.T) {
	parent := t.TempDir()
	state := filepath.Join(parent, "tasks")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(validMetadata), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(state, filepath.Join(parent, "old-tasks")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := strings.Replace(validMetadata, "spawn_gen=s123.456.7", "spawn_gen=s123.456.8", 1)
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	id, _ := ParseTaskID("task-1")
	metadata, err := ReadTaskFromRoot(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SpawnGeneration() != "s123.456.7" {
		t.Fatalf("read replacement directory: %s", metadata.SpawnGeneration())
	}
}

func TestParseLocalHerdrMetadata(t *testing.T) {
	id, err := ParseTaskID("task-1")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := Parse([]byte("note=first\nnote=last\n"+validMetadata), id)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.TaskID() != id || metadata.Project().String() != "/repo" ||
		metadata.Worktree().String() != "/repo/worktree" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.Window() != "crew:pane-1" || metadata.HerdrSession() != "crew" ||
		metadata.HerdrPaneID() != "pane-1" ||
		metadata.HerdrWorkspaceID() != "workspace-1" ||
		metadata.HerdrTabID() != "tab:1" {
		t.Fatalf("unexpected endpoint: %+v", metadata)
	}
	if metadata.Harness() != "codex" || metadata.Kind() != "ship" ||
		metadata.SpawnGeneration() != "s123.456.7" {
		t.Fatalf("unexpected task fields: %+v", metadata)
	}
}

func TestParseRejectsUnsafeMetadata(t *testing.T) {
	id, err := ParseTaskID("task-1")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, metadata string }{
		{"missing binding", strings.Replace(validMetadata, "endpoint_task_id=task-1\n", "", 1)},
		{
			"wrong binding",
			strings.Replace(validMetadata, "endpoint_task_id=task-1", "endpoint_task_id=task-2", 1),
		},
		{"duplicate binding", validMetadata + "endpoint_task_id=task-1\n"},
		{"duplicate endpoint", validMetadata + "herdr_pane_id=pane-2\n"},
		{"remote host", validMetadata + "remote_host=host.example\n"},
		{"empty remote host", validMetadata + "remote_host=\n"},
		{"wrong backend", strings.Replace(validMetadata, "backend=herdr", "backend=tmux", 1)},
		{"missing backend", strings.Replace(validMetadata, "backend=herdr\n", "", 1)},
		{
			"window mismatch",
			strings.Replace(validMetadata, "window=crew:pane-1", "window=other:pane-1", 1),
		},
		{
			"relative worktree",
			strings.Replace(validMetadata, "worktree=/repo/worktree", "worktree=repo/worktree", 1),
		},
		{"relative project", strings.Replace(validMetadata, "project=/repo", "project=repo", 1)},
		{
			"malformed session",
			strings.Replace(validMetadata, "herdr_session=crew", "herdr_session=bad:session", 1),
		},
		{
			"malformed tab",
			strings.Replace(validMetadata, "herdr_tab_id=tab:1", "herdr_tab_id=bad/tab", 1),
		},
		{
			"missing spawn generation",
			strings.Replace(validMetadata, "spawn_gen=s123.456.7\n", "", 1),
		},
		{"missing harness", strings.Replace(validMetadata, "harness=codex\n", "", 1)},
		{"missing kind", strings.Replace(validMetadata, "kind=ship\n", "", 1)},
		{
			"control character",
			strings.Replace(validMetadata, "herdr_pane_id=pane-1", "herdr_pane_id=pane\t1", 1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse([]byte(test.metadata), id)
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("Parse() = %+v, %v; want invalid metadata", got, err)
			}
		})
	}
}

func TestTaskIDAndAbsolutePathFactories(t *testing.T) {
	for _, value := range []string{"", "a/b", "a:b", "a\nb"} {
		if _, err := ParseTaskID(value); err == nil {
			t.Errorf("ParseTaskID(%q) succeeded", value)
		}
	}
	for _, value := range []string{"", "relative", "/a/../b", "/a\tb", "/a\u0085b"} {
		if _, err := ParseAbsolutePath(value); err == nil {
			t.Errorf("ParseAbsolutePath(%q) succeeded", value)
		}
	}
	if _, err := ParseAbsolutePath("/a b/project"); err != nil {
		t.Fatal(err)
	}
}

func TestReadTaskSelectsBoundMetadata(t *testing.T) {
	id, err := ParseTaskID("task-1")
	if err != nil {
		t.Fatal(err)
	}
	stateDir, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir.String(), "task-1.meta")
	if err := os.WriteFile(path, []byte(validMetadata), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir.String(), "task-2.meta"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata, err := ReadTask(stateDir, id)
	if err != nil || metadata.TaskID() != id {
		t.Fatalf("ReadTask() = %+v, %v", metadata, err)
	}
	if _, err := ReadTask(AbsolutePath{}, id); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("zero state path error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(stateDir.String(), "task-2.meta"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTask(stateDir, id); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("symlink error = %v", err)
	}
}
