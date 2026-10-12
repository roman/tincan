package conciergecli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func legacyStateFixture(t *testing.T) (string, string) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backlog.md"), []byte("# Tasks\n## Queued\n- [ ] task-1 - Work (repo: exmachina) (kind: ship)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(parent, "backup")
}

func TestStateBackupAndMigration(t *testing.T) {
	root, backup := legacyStateFixture(t)
	t.Setenv("TINCAN_HOME", "unused-relative-path")
	for _, test := range []struct {
		operation string
		version   int
	}{{"backup", 1}, {"migrate", 2}} {
		var output bytes.Buffer
		code := Run(
			context.Background(),
			[]string{"--root", root, "state", test.operation, "--backup", backup},
			strings.NewReader(""),
			&output,
			io.Discard,
		)
		if code != 0 ||
			output.String() != fmt.Sprintf(
				"version: %d\nbackup: %s\n",
				test.version,
				quote(backup),
			) {
			t.Fatalf("%s: code %d output %s", test.operation, code, &output)
		}
	}
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
}

func TestStateBackupRefusesExistingDestination(t *testing.T) {
	root, backup := legacyStateFixture(t)
	if err := os.Mkdir(backup, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(backup, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "state", "backup", "--backup", backup},
		nil,
		&output,
		io.Discard,
	)
	if code != 1 || strings.Contains(output.String(), "version:") {
		t.Fatalf("code %d output %s", code, &output)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "keep" {
		t.Fatalf("destination changed: %q %v", data, err)
	}
}

func TestStateRecoverPendingMigration(t *testing.T) {
	root, backup := legacyStateFixture(t)
	if _, err := taskstate.BackupLegacy(context.Background(), root, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "records"), 0700); err != nil {
		t.Fatal(err)
	}
	task, err := taskstate.NewQueued(
		taskstate.Definition{ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship"},
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := task.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{"records/.migration-owner": []byte("tincan-taskstate-v2\n"), "records/task-1.json": data, ".task-state-migration-pending": []byte("2\n")} {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "state", "recover", "--backup", backup},
		nil,
		&output,
		io.Discard,
	)
	if code != 0 || output.String() != "version: 2\nbackup: "+quote(backup)+"\n" {
		t.Fatalf("code %d output %s", code, &output)
	}
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
}

func TestStateUsageAndHelp(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		code int
	}{
		{"bare", []string{"state"}, 2},
		{"missing backup", []string{"state", "backup"}, 2},
		{"relative backup", []string{"state", "migrate", "--backup", "relative"}, 2},
		{"help", []string{"--root", "relative", "state", "--help"}, 0},
		{"recover help", []string{"--root", "relative", "state", "recover", "--help"}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			code := Run(context.Background(), test.args, nil, &output, io.Discard)
			if code != test.code {
				t.Fatalf("code %d want %d output %s", code, test.code, &output)
			}
		})
	}
}

func TestStateOutputFailure(t *testing.T) {
	root, backup := legacyStateFixture(t)
	code := Run(
		context.Background(),
		[]string{"--root", root, "state", "backup", "--backup", backup},
		nil,
		failedOutput{},
		io.Discard,
	)
	if code != 1 {
		t.Fatalf("code %d want 1", code)
	}
}
