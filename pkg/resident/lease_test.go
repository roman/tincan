package resident

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTaskLeaseAllowsOneResidentController(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireTaskLease(root, "task-1"); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("duplicate controller = %v", err)
	}
	other, err := AcquireTaskLease(root, "task-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskLeaseRejectsSymlinkedLock(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "other.lock")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "task-1.controller.lock")); err != nil {
		t.Fatal(err)
	}
	if lease, err := AcquireTaskLease(root, "task-1"); err == nil {
		lease.Close()
		t.Fatal("symlinked lock was accepted")
	}
}
