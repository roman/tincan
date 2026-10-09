package firstmate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateTaskMetadataLockedPublishesAndRetries(t *testing.T) {
	state := t.TempDir()
	root, err := os.OpenRoot(state)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id, _ := ParseTaskID("task-1")
	if published, err := CreateTaskMetadataLocked(root, id, []byte(validMetadata)); err != nil ||
		!published {
		t.Fatalf("create: published=%v err=%v", published, err)
	}
	path := filepath.Join(state, "task-1.meta")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != validMetadata {
		t.Fatalf("published metadata=%q err=%v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("published mode=%v", info.Mode())
	}
	if published, err := CreateTaskMetadataLocked(root, id, []byte(validMetadata)); err != nil ||
		published {
		t.Fatalf("retry: published=%v err=%v", published, err)
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 1 || entries[0].Name() != "task-1.meta" {
		t.Fatalf("directory entries=%v err=%v", entries, err)
	}
}

func TestCreateTaskMetadataLockedRefusesConflictsAndUnsafeTargets(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing string
		input    string
		wantErr  error
	}{
		{name: "invalid input", input: "endpoint_task_id=task-1\n", wantErr: ErrInvalidMetadata},
		{name: "oversized metadata", input: validMetadata + "padding=" + strings.Repeat("a", 1<<20),
			wantErr: ErrInvalidMetadata},
		{name: "different metadata", existing: validMetadata,
			input:   strings.Replace(validMetadata, "spawn_gen=s123.456.7", "spawn_gen=s123.456.8", 1),
			wantErr: ErrMetadataConflict},
		{name: "symlink target", existing: "symlink", input: validMetadata,
			wantErr: ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := t.TempDir()
			path := filepath.Join(state, "task-1.meta")
			switch test.existing {
			case "":
			case "symlink":
				if err := os.Symlink(filepath.Join(state, "target"), path); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, []byte(test.existing), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(state)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			id, _ := ParseTaskID("task-1")
			if published, err := CreateTaskMetadataLocked(root, id, []byte(test.input)); published ||
				!errors.Is(err, test.wantErr) {
				t.Fatalf("published=%v err=%v, want %v", published, err, test.wantErr)
			}
			entries, err := os.ReadDir(state)
			if err != nil {
				t.Fatal(err)
			}
			if test.existing == "" {
				if len(entries) != 0 {
					t.Fatalf("invalid input left entries: %v", entries)
				}
				return
			}
			if len(entries) != 1 || entries[0].Name() != "task-1.meta" {
				t.Fatalf("conflict changed entries: %v", entries)
			}
			if test.existing == "symlink" {
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink target changed: info=%v", info)
				}
				return
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != test.existing {
				t.Fatalf("existing metadata changed: %q, err=%v", data, err)
			}
		})
	}
}
