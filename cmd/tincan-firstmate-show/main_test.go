package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const taskMetadata = "window=crew:pane-1\nendpoint_task_id=task-1\nworktree=/repo/worktree\nproject=/repo\nharness=codex\nkind=ship\nspawn_gen=s123.456.7\nbackend=herdr\nherdr_session=crew\nherdr_workspace_id=workspace-1\nherdr_tab_id=tab:1\nherdr_pane_id=pane-1\n"

func TestShowReportsCurrentTaskAndRecord(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(taskMetadata), 0600); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(state, "task-1.inbox")
	if err := os.Mkdir(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "001.msg"), []byte("schema=fm-task-inbox.v1\n--\n"+strings.Repeat("x", 1000)), 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	code := run([]string{"--state", state, "task-1", "001.msg"}, &output, &diagnostic)
	if code != 0 || !strings.Contains(output.String(), "spawn_generation: s123.456.7") ||
		!strings.Contains(output.String(), "pane_id: pane-1") ||
		!strings.Contains(output.String(), "record_state: pending") ||
		!strings.Contains(output.String(), "truncated: true") {
		t.Fatalf("code=%d output=%q diagnostic=%q", code, output.String(), diagnostic.String())
	}
	output.Reset()
	code = run([]string{"--state", state, "--full", "task-1", "001.msg"}, &output, &diagnostic)
	if code != 0 || !strings.Contains(output.String(), strings.Repeat("x", 1000)) ||
		!strings.Contains(output.String(), "truncated: false") {
		t.Fatalf("full code=%d output=%q diagnostic=%q", code, output.String(), diagnostic.String())
	}
}
