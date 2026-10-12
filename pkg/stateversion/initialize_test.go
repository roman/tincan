package stateversion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitializationPublishesAndRetries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	guard, state, err := AcquireInitialization(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if state != FreshRoot || guard.StateRoot() == nil {
		t.Fatalf("state %v root %v", state, guard.StateRoot())
	}
	if err := guard.PublishInitialVersion(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	guard, state, err = AcquireInitialization(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if state != ExistingVersionTwo {
		t.Fatalf("state %v", state)
	}
	if err := guard.PublishInitialVersion(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, pendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initialization created migration barrier: %v", err)
	}
}

func TestInitializationRejectsVersionAndPending(t *testing.T) {
	for _, test := range []struct{ name, file, contents string }{
		{"explicit legacy", versionFile, "1\n"},
		{"malformed", versionFile, "invalid\n"},
		{"unsupported", versionFile, "3\n"},
		{"pending", pendingFile, "2\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, test.file)
			if err := os.WriteFile(path, []byte(test.contents), 0600); err != nil {
				t.Fatal(err)
			}
			guard, _, err := AcquireInitialization(context.Background(), root)
			if err == nil {
				guard.Close()
				t.Fatal("accepted incompatible state")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != test.contents {
				t.Fatalf("state changed: %q %v", data, err)
			}
		})
	}
}

func TestInitializationSerializesWithTaskReaders(t *testing.T) {
	root := t.TempDir()
	reader, err := AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	guard, _, err := AcquireInitialization(ctx, root)
	if err == nil {
		guard.Close()
		t.Fatal("initialization bypassed reader lease")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, versionFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked initialization published marker: %v", err)
	}
}

func TestInitializationPublicationSyncFailureAndRetry(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(map[int]string{1: "root", 2: "parent"}[failAt], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			guard, _, err := AcquireInitialization(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			syncFailure := errors.New("sync failed")
			calls := 0
			guard.syncDirectory = func(*os.Root) error {
				calls++
				if calls == failAt {
					return syncFailure
				}
				return nil
			}
			if err := guard.PublishInitialVersion(); !errors.Is(err, syncFailure) ||
				!errors.Is(err, ErrPublicationUnconfirmed) {
				t.Fatalf("error %v", err)
			}
			if err := guard.Close(); err != nil {
				t.Fatal(err)
			}
			guard, state, err := AcquireInitialization(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			if state != ExistingVersionTwo {
				t.Fatalf("state %v", state)
			}
			calls = 0
			guard.syncDirectory = func(*os.Root) error { calls++; return nil }
			if err := guard.PublishInitialVersion(); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("retry synced %d directories", calls)
			}
		})
	}
}

func TestSharedGuardCannotInitialize(t *testing.T) {
	guard, err := AcquireLegacy(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := guard.PublishInitialVersion(); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("error %v", err)
	}
}

func TestInitializationRequiresRealRootAndExistingParent(t *testing.T) {
	parent := t.TempDir()
	if err := os.Symlink(parent, filepath.Join(parent, "alias")); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(parent, "alias"), filepath.Join(parent, "missing", "state")} {
		guard, _, err := AcquireInitialization(context.Background(), root)
		if err == nil {
			guard.Close()
			t.Fatalf("accepted root %s", root)
		}
	}
}

func TestGuardPublicationRequiresMatchingPurpose(t *testing.T) {
	for _, test := range []struct {
		name    string
		acquire func(context.Context, string) (*Guard, error)
		publish func(*Guard) error
	}{
		{"migration cannot initialize", AcquireMigration, (*Guard).PublishInitialVersion},
		{"initialization cannot migrate", func(ctx context.Context, root string) (*Guard, error) {
			guard, _, err := AcquireInitialization(ctx, root)
			return guard, err
		}, func(guard *Guard) error { return guard.PublishVersion(2) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			guard, err := test.acquire(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer guard.Close()
			if err := test.publish(guard); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("mismatched publication error: %v", err)
			}
			for _, name := range []string{versionFile, pendingFile} {
				if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("mismatched publication changed %s: %v", name, err)
				}
			}
		})
	}
}
