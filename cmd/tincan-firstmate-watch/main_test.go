package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
)

const taskMetadata = "window=crew:pane-1\nendpoint_task_id=task-1\nworktree=/repo/worktree\nproject=/repo\nharness=codex\nkind=ship\nspawn_gen=s123.456.7\nbackend=herdr\nherdr_session=crew\nherdr_workspace_id=workspace-1\nherdr_tab_id=tab:1\nherdr_pane_id=pane-1\n"

func runWatch(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRingFailureReportsDeliveryError(t *testing.T) {
	var output bytes.Buffer
	reports := []firstmate.SweepReport{{
		Task: "task-1",
		Result: firstmate.CheckResult{
			Outcome:  firstmate.CheckRung,
			Delivery: herdr.RingResult{Outcome: herdr.RingFailed, Err: os.ErrNotExist},
		},
	}}
	if code := printReports(&output, reports, reportResident); code != 1 {
		t.Fatalf("exit code=%d", code)
	}
	if !strings.Contains(output.String(), "task-1,ring,,failed") ||
		!strings.Contains(output.String(), "file does not exist") {
		t.Fatalf("output=%q", output.String())
	}
}

type sweepSignalWriter struct{ signals chan struct{} }

func (writer sweepSignalWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("errors[1]")) {
		writer.signals <- struct{}{}
	}
	return len(data), nil
}

func TestResidentWatchRepeatsAndStopsCleanly(t *testing.T) {
	state := t.TempDir()
	if err := os.Symlink("missing", filepath.Join(state, "task-1.meta")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan struct{}, 2)
	finished := make(chan int, 1)
	go func() {
		finished <- run(ctx, []string{
			"--state", state, "--wake-root", t.TempDir(), "--interval", "5ms",
		}, sweepSignalWriter{signals: signals}, &bytes.Buffer{})
	}()
	for range 2 {
		select {
		case <-signals:
		case <-time.After(2 * time.Second):
			t.Fatal("watch did not repeat")
		}
	}
	cancel()
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("shutdown code=%d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not stop")
	}
}

func TestWatchOncePublishesEscalation(t *testing.T) {
	state := t.TempDir()
	wakeRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(taskMetadata), 0600); err != nil {
		t.Fatal(err)
	}
	taskInbox := filepath.Join(state, "task-1.inbox")
	if err := os.Mkdir(taskInbox, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(taskInbox, "001.msg")
	if err := os.WriteFile(record, []byte("schema=fm-task-inbox.v1\n--\nhello"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(record, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(taskInbox, ".ring-state"), []byte("001.msg\t3\t0\n"), 0600,
	); err != nil {
		t.Fatal(err)
	}
	code, output, diagnostic := runWatch(
		"--state", state, "--wake-root", wakeRoot,
		"--herdr", filepath.Join(t.TempDir(), "absent-herdr"),
		"--grace-seconds", "0", "--once",
	)
	if code != 0 || !strings.Contains(output, "task-1,escalate,001.msg") {
		t.Fatalf("code=%d output=%q diagnostic=%q", code, output, diagnostic)
	}
	store, err := inbox.New(wakeRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		t.Fatal(err)
	}
	wakes, err := store.List(recipient)
	if err != nil || len(wakes) != 1 {
		t.Fatalf("wakes=%v error=%v", wakes, err)
	}
	marker, err := os.ReadFile(filepath.Join(taskInbox, ".escalated"))
	if err != nil || string(marker) != "001.msg\n" {
		t.Fatalf("marker=%q error=%v", marker, err)
	}
}

func TestWatchRefusesSecondRetryWriter(t *testing.T) {
	state := t.TempDir()
	statePath, err := firstmate.ParseAbsolutePath(state)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := firstmate.AcquireRetryWriter(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	code, _, diagnostic := runWatch("--state", state, "--wake-root", t.TempDir(), "--once")
	if code != 1 || !strings.Contains(diagnostic, "another Tincan writer") {
		t.Fatalf("code=%d diagnostic=%q", code, diagnostic)
	}
}
