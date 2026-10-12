package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/stateversion"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestMigrateCommandPublishesVersionTwo(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	backlog := "# Tasks\n## Queued\n- [ ] task-1 - Work (repo: exmachina) (kind: ship)\n"
	if err := os.WriteFile(filepath.Join(root, "backlog.md"), []byte(backlog), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(parent, "backup")
	var output bytes.Buffer
	if code := run(context.Background(), []string{"--root", root, "--backup", backup, "migrate"}, &output); code != 0 {
		t.Fatalf("migration returned %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "version: 2") ||
		!strings.Contains(output.String(), "backup: "+strconv.Quote(backup)) {
		t.Fatalf("migration output omitted the state and backup: %s", output.String())
	}
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateCommandRejectsRelativeBackup(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	if code := run(context.Background(), []string{"--root", root, "--backup", "backup", "migrate"}, &output); code != 2 {
		t.Fatalf("relative backup returned %d, want usage error: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "use an absolute, clean --backup path") {
		t.Fatalf("migration omitted the backup-path hint: %s", output.String())
	}
}

func TestBackupCommandKeepsVersionOne(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backlog.md"), []byte("# Tasks\n## Queued\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(parent, "backup")
	var output bytes.Buffer
	if code := run(context.Background(), []string{"--root", root, "--backup", backup, "backup"}, &output); code != 0 {
		t.Fatalf("backup returned %d: %s", code, output.String())
	}
	guard, err := stateversion.AcquireLegacy(context.Background(), root)
	if err != nil {
		t.Fatalf("backup changed the task-state version: %v", err)
	}
	guard.Close()
	if _, err := os.Stat(filepath.Join(backup, "manifest.json")); err != nil {
		t.Fatalf("backup lacks a manifest: %v", err)
	}
}

func TestMigrateCommandHelpAndVersion(t *testing.T) {
	for _, flag := range []string{"-v", "-V", "--version"} {
		var output bytes.Buffer
		if code := run(context.Background(), []string{flag}, &output); code != 0 ||
			output.String() != "0.1.0\n" {
			t.Fatalf("version %q returned %d: %s", flag, code, output.String())
		}
	}
	for _, arguments := range [][]string{nil, {"--help"}} {
		var output bytes.Buffer
		if code := run(context.Background(), arguments, &output); code != 0 {
			t.Fatalf("help %v returned %d: %s", arguments, code, output.String())
		}
		if !strings.Contains(output.String(), "concierge-migrate") {
			t.Fatalf("help %v omitted the command: %s", arguments, output.String())
		}
	}
	for _, test := range []struct {
		arguments []string
		command   string
	}{
		{[]string{"backup", "--help"}, "backup"},
		{[]string{"migrate", "--help"}, "migrate"},
		{[]string{"recover", "--help"}, "recover"},
		{[]string{"--root", "/tmp/state", "--backup", "/tmp/backup", "migrate", "--help"}, "migrate"},
	} {
		var output bytes.Buffer
		if code := run(context.Background(), test.arguments, &output); code != 0 {
			t.Fatalf("help %v returned %d: %s", test.arguments, code, output.String())
		}
		example := "concierge-migrate --root /path/state --backup /path/state-backup " + test.command
		if !strings.Contains(output.String(), example) {
			t.Fatalf("help %v omitted its example: %s", test.arguments, output.String())
		}
	}
}
