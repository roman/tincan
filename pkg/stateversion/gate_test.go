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
	if _, err := AcquireMigration(ctx, root); !errors.Is(err, ErrLockTimeout) {
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
	if _, err := AcquireMigration(ctx, root); !errors.Is(err, ErrLockTimeout) {
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
	for _, name := range []string{lockSuffix, versionFile} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			selected := filepath.Join(root, name)
			if name == lockSuffix {
				selected = filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+lockSuffix)
			}
			target := filepath.Join(filepath.Dir(selected), "target")
			if err := os.WriteFile(target, []byte("1\n"), 0o600); err != nil {
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
			if _, err := AcquireLegacy(context.Background(), root); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("legacy command accepted version %q: %v", version, err)
			}
		})
	}
}
