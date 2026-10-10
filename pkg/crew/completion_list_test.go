package crew

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPendingCompletionIgnoresMarkerRemovedAfterListing(t *testing.T) {
	root := t.TempDir()
	taskRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer taskRoot.Close()
	completion, found, err := pendingCompletion(taskRoot, "task-1.completion.json")
	if err != nil || found || completion != (PendingCompletion{}) {
		t.Fatalf("completion=%+v found=%t err=%v", completion, found, err)
	}
}

func TestListPendingCompletionsRejectsMalformedMarker(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "tasks")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "task-1.completion.json"), []byte(`{"mode":"close"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ListPendingCompletions(); err == nil {
		t.Fatal("malformed marker accepted")
	}
}
