package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListShowsAllQueuedBacklogRows(t *testing.T) {
	root := t.TempDir()
	var backlog strings.Builder
	backlog.WriteString("# Backlog\n\n## In flight\n## Queued\n")
	for index := 1; index <= 101; index++ {
		fmt.Fprintf(
			&backlog,
			"- [ ] task-%03d - Work %d (repo: exmachina) (kind: task)\n",
			index,
			index,
		)
	}
	backlog.WriteString("## Done\n- [x] task-old - Done work (repo: exmachina) (kind: task)\n")
	if err := os.WriteFile(filepath.Join(root, "backlog.md"), []byte(backlog.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root, "list"}, &output); code != 0 {
		t.Fatalf("list returned %d: %s", code, output.String())
	}
	for _, expected := range []string{"count: 101", "task-001,queued", "task-101,queued"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in %s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "task-old") ||
		strings.Contains(output.String(), "tasks-axi") {
		t.Fatalf("list exposed completed work or dependency hints: %s", output.String())
	}
	output.Reset()
	if code := run([]string{"--root", root, "list", "--state", "done"}, &output); code != 0 ||
		!strings.Contains(output.String(), "task-old,done") ||
		strings.Contains(output.String(), "task-001,queued") {
		t.Fatalf("done list returned %d: %s", code, output.String())
	}
}

func TestShowReadsFullBacklogEntry(t *testing.T) {
	root := t.TempDir()
	title := "Inspect " + strings.TrimSpace(strings.Repeat("long task details ", 8))
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	body := strings.Repeat("Detailed context. ", 100) + "END-OF-BODY"
	if err := os.WriteFile(bodyFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"--root", root, "add", "task-long", title,
		"--kind", "task", "--repo", "exmachina", "--body-file", bodyFile}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("add returned %d", code)
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root, "show", "task-long", "--full"}, &output); code != 0 {
		t.Fatalf("show returned %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "title: "+title) {
		t.Fatalf("show omitted the complete title: %s", output.String())
	}
	if !strings.Contains(output.String(), "END-OF-BODY") {
		t.Fatalf("show omitted the end of the body: %s", output.String())
	}
}

func TestShowReportsMissingTask(t *testing.T) {
	root := t.TempDir()
	backlog := "# Backlog\n\n## Queued\n- [ ] existing - Existing task (repo: exmachina) (kind: task)\n"
	if err := os.WriteFile(filepath.Join(root, "backlog.md"), []byte(backlog), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run([]string{"--root", root, "show", "missing"}, &output); code == 0 {
		t.Fatalf("show unexpectedly succeeded: %s", output.String())
	}
	if !strings.Contains(output.String(), "not found") {
		t.Fatalf("show omitted the missing-task reason: %s", output.String())
	}
}

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

func TestLegacyBacklogRefusesMigratedState(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".task-state-version"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := run([]string{"--root", root, "add", "task-1", "New work",
		"--kind", "task", "--repo", "exmachina"}, &output)
	if code == 0 || !strings.Contains(output.String(), "task-state version") {
		t.Fatalf("legacy command accepted migrated state: code=%d output=%s", code, output.String())
	}
	if _, err := os.Stat(filepath.Join(root, "backlog.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy command changed migrated state: %v", err)
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
