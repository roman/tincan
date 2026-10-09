package firstmate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListTasksReadsValidMetadataInNameOrder(t *testing.T) {
	state := t.TempDir()
	for _, task := range []string{"task-2", "task-1"} {
		metadata := strings.ReplaceAll(validMetadata, "task-1", task)
		if err := os.WriteFile(filepath.Join(state, task+".meta"), []byte(metadata), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "readme"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := ParseAbsolutePath(state)
	tasks, err := ListTasks(path)
	if err != nil || len(tasks) != 2 || tasks[0].TaskID().String() != "task-1" ||
		tasks[1].TaskID().String() != "task-2" {
		t.Fatalf("tasks=%v err=%v", tasks, err)
	}
}

func TestListTasksHandlesMissingStateAndRejectsUnsafeMetadata(t *testing.T) {
	parent := t.TempDir()
	missing, _ := ParseAbsolutePath(filepath.Join(parent, "missing"))
	if tasks, err := ListTasks(missing); err != nil || len(tasks) != 0 {
		t.Fatalf("missing tasks=%v err=%v", tasks, err)
	}
	state := filepath.Join(parent, "tasks")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "target"), filepath.Join(state, "task-1.meta")); err != nil {
		t.Fatal(err)
	}
	path, _ := ParseAbsolutePath(state)
	if _, err := ListTasks(path); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("symlink metadata error=%v", err)
	}
}

func TestListTasksRejectsOversizedMetadata(t *testing.T) {
	state := t.TempDir()
	metadata := validMetadata + "padding=" + strings.Repeat("a", 1<<20) + "\n"
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := ParseAbsolutePath(state)
	if _, err := ListTasks(path); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized metadata error=%v", err)
	}
}
