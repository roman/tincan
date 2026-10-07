package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNewRejectsInvalidRoot(t *testing.T) {
	for _, raw := range []string{"", "\x00"} {
		if _, err := New(raw); !errors.Is(err, ErrInvalidRoot) {
			t.Fatalf("root %q: %v", raw, err)
		}
	}
}

func TestNewPreservesLazyDirectoryCreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor created root: %v", err)
	}
	messages, err := store.List(mustRecipient(t, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("pending messages: %d", len(messages))
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("list created root: %v", err)
	}
}
