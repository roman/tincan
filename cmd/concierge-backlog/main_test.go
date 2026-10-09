package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddCreatesQueuedTaskInRootBacklog(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".tincan")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--root", root, "add", "tc-dependency-a1", "Dependency", "--kind", "ship", "--repo", "exmachina"}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("dependency add returned %d", code)
	}
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("Detailed intake instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := run(
		[]string{
			"--root",
			root,
			"add",
			"tc-intake-a1",
			"Test intake",
			"--kind",
			"ship",
			"--repo",
			"exmachina",
			"--body-file",
			bodyFile,
			"--blocked-by",
			"tc-dependency-a1",
		},
		&output,
	)
	if code != 0 {
		t.Fatalf("add returned %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "state: queued") {
		t.Fatalf("missing queued state: %s", output.String())
	}
	backlog, err := os.ReadFile(filepath.Join(root, "backlog.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(backlog), "tc-intake-a1 - Test intake") {
		t.Fatalf("missing backlog task: %s", backlog)
	}
	if !strings.Contains(string(backlog), "Detailed intake instructions") ||
		!strings.Contains(
			string(backlog),
			"tc-intake-a1 - Test intake blocked-by: tc-dependency-a1",
		) {
		t.Fatalf("missing task body or dependency: %s", backlog)
	}
}

func TestAddRejectsDuplicateWithoutChangingTask(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".tincan")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	first := []string{
		"--root",
		root,
		"add",
		"tc-intake-a1",
		"Original",
		"--kind",
		"ship",
		"--repo",
		"exmachina",
	}
	if code := run(first, &bytes.Buffer{}); code != 0 {
		t.Fatalf("first add returned %d", code)
	}
	var output bytes.Buffer
	duplicate := []string{
		"--root",
		root,
		"add",
		"tc-intake-a1",
		"Replacement",
		"--kind",
		"ship",
		"--repo",
		"exmachina",
	}
	if code := run(duplicate, &output); code == 0 {
		t.Fatalf("duplicate add succeeded: %s", output.String())
	}
	backlog, err := os.ReadFile(filepath.Join(root, "backlog.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(backlog), "Original") ||
		strings.Contains(string(backlog), "Replacement") {
		t.Fatalf("duplicate changed backlog: %s", backlog)
	}
}
