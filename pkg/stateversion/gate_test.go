package stateversion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyGuardAcceptsUnmarkedState(t *testing.T) {
	root := t.TempDir()
	guard, err := AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyGuardAcceptsAbsentRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	guard, err := AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only version check created the state root: %v", err)
	}
}

func TestMigrationGuardRejectsAbsentRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if guard, err := AcquireMigration(context.Background(), root); err == nil {
		guard.Close()
		t.Fatal("migration accepted an absent state root")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration created the state root: %v", err)
	}
}

func TestVersionTwoGuardRequiresPublishedMarker(t *testing.T) {
	root := t.TempDir()
	if _, err := AcquireVersionTwo(context.Background(), root); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("version 2 command accepted unmarked state: %v", err)
	}
	migration, err := AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.PublishVersion(2); err != nil {
		t.Fatal(err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	guard, err := AcquireVersionTwo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if guard.StateRoot() == nil {
		t.Fatal("version 2 guard has no selected state directory")
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyGuardRejectsSymlinkRoot(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink("target", alias); err != nil {
		t.Fatal(err)
	}
	if guard, err := AcquireLegacy(context.Background(), alias); err == nil {
		guard.Close()
		t.Fatal("legacy command accepted a symlinked state root")
	}
}

func TestRootReplacementDoesNotSplitVersionLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := AcquireMigration(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("migration acquired a split lock: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	migration, err := AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationGuardWaitsForReaders(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := AcquireMigration(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("migration acquired lock while readers ran: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	migration, err := AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.PublishVersion(2); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(root, versionFile))
	if err != nil || string(marker) != "2\n" {
		t.Fatalf("published marker = %q, %v; want 2\\n", marker, err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLegacy(context.Background(), root); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("legacy command accepted migrated state: %v", err)
	}
	if _, err := AcquireMigration(context.Background(), root); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("migration accepted version 2: %v", err)
	}
}

func TestVersionGuardRespectsCancelledContextBeforeFreeLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	if guard, err := AcquireLegacy(ctx, root); !errors.Is(err, context.Canceled) {
		if guard != nil {
			guard.Close()
		}
		t.Fatalf("AcquireLegacy error = %v, want cancellation", err)
	}
}

func TestMigrationCanConfirmMarkerAfterSyncFailure(t *testing.T) {
	root := t.TempDir()
	migration, err := AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("injected directory sync failure")
	syncCount := 0
	migration.syncDirectory = func(root *os.Root) error {
		syncCount++
		if syncCount == 2 {
			return syncFailure
		}
		return syncStateDirectory(root)
	}
	if err := migration.PublishVersion(2); !errors.Is(err, ErrPublicationUnconfirmed) ||
		!errors.Is(err, syncFailure) {
		t.Fatalf("PublishVersion error = %v, want unconfirmed marker and sync cause", err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireMigration(context.Background(), root); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("version 1 migration resumed after marker publication: %v", err)
	}
	if _, err := AcquireVersionTwo(context.Background(), root); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("version 2 command entered pending migration: %v", err)
	}
	recovery, err := AcquireRecovery(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.ConfirmPublishedVersion(); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Close(); err != nil {
		t.Fatal(err)
	}
	ready, err := AcquireVersionTwo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ready.Close()
}

func TestMigrationRecoveryBeforeVersionMarkerChanges(t *testing.T) {
	root := t.TempDir()
	migration, err := AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("pending marker sync failure")
	migration.syncDirectory = func(*os.Root) error { return syncFailure }
	if err := migration.PublishVersion(2); !errors.Is(err, syncFailure) ||
		!errors.Is(err, ErrPublicationUnconfirmed) {
		t.Fatalf("PublishVersion error = %v, want unconfirmed pending publication", err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLegacy(context.Background(), root); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("legacy command entered pending migration: %v", err)
	}
	recovery, err := AcquireRecovery(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.PublishVersion(2); err != nil {
		t.Fatal(err)
	}
	if err := recovery.Close(); err != nil {
		t.Fatal(err)
	}
	ready, err := AcquireVersionTwo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ready.Close()
}

func TestMigrationReportsCleanupSyncFailureAfterVersionTwoIsReady(t *testing.T) {
	root := t.TempDir()
	migration, err := AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("cleanup sync failure")
	syncCount := 0
	migration.syncDirectory = func(root *os.Root) error {
		syncCount++
		if syncCount == 3 {
			return syncFailure
		}
		return syncStateDirectory(root)
	}
	if err := migration.PublishVersion(2); !errors.Is(err, ErrCleanupUnconfirmed) ||
		!errors.Is(err, syncFailure) {
		t.Fatalf("PublishVersion error = %v, want unconfirmed cleanup", err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, pendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending marker after removal = %v, want absent", err)
	}
	ready, err := AcquireVersionTwo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ready.Close()
	if _, err := AcquireRecovery(context.Background(), root); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("recovery accepted removed marker: %v", err)
	}
}

func TestSharedGuardCannotPublishVersion(t *testing.T) {
	root := t.TempDir()
	guard, err := AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := guard.PublishVersion(2); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("shared guard published a version: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, versionFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared guard changed the marker: %v", err)
	}
}

func TestSymlinkedGateFilesAreRejected(t *testing.T) {
	for _, name := range []string{lockSuffix, versionFile, pendingFile} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			selected := filepath.Join(root, name)
			if name == lockSuffix {
				selected = filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+lockSuffix)
			}
			target := filepath.Join(filepath.Dir(selected), "target")
			content := "1\n"
			if name == pendingFile {
				content = "2\n"
			}
			if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("target", selected); err != nil {
				t.Fatal(err)
			}
			if guard, err := AcquireLegacy(context.Background(), root); err == nil {
				guard.Close()
				t.Fatal("legacy command accepted a symlinked gate file")
			}
		})
	}
}

func TestMalformedVersionRefusesLegacy(t *testing.T) {
	for _, version := range []string{"", "3\n", "1\n2\n", "v1\n"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, versionFile), []byte(version), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := AcquireLegacy(context.Background(), root); !errors.Is(
				err,
				ErrIncompatible,
			) {
				t.Fatalf("legacy command accepted version %q: %v", version, err)
			}
		})
	}
}
