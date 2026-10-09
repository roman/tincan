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
)

const checkMetadata = "window=crew:pane-1\nendpoint_task_id=task-1\nworktree=/repo/worktree\nproject=/repo\nharness=codex\nkind=ship\nspawn_gen=s123.456.7\nbackend=herdr\nherdr_session=crew\nherdr_workspace_id=workspace-1\nherdr_tab_id=tab:1\nherdr_pane_id=pane-1\n"

const checkDoorbell = `: Firstmate instruction waiting: list "$FM_TASK_INBOX"/*.msg in your 'task-1.inbox' steering inbox, read and act on each in numeric order, then mv each into its handled/.`

func checkCLIFixture(t *testing.T) (string, string) {
	t.Helper()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(checkMetadata), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "herdr.log")
	executable := filepath.Join(t.TempDir(), "herdr")
	script := `#!/bin/sh
printf 'session=%s args=%s\n' "$HERDR_SESSION" "$*" >> "$HERDR_LOG"
case "$1 $2" in
  'pane get')
    if [ "$HERDR_MODE" = missing ]; then printf '{"error":{"code":"pane_not_found"}}'
    else printf '{"result":{"pane":{"pane_id":"pane-1"}}}'; fi ;;
  'agent get')
    if [ "$HERDR_MODE" = busy ]; then status=working; else status=idle; fi
    printf '{"result":{"agent":{"pane_id":"pane-1","agent":"codex","agent_status":"%s"}}}' "$status" ;;
  'pane process-info')
    printf '{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}' ;;
  'pane read')
    if [ "$HERDR_MODE" = old ] && [ ! -e "$HERDR_ENTER_MARK" ]; then
      printf '› %s\n' "$HERDR_DOORBELL"
    else printf '› \n'; fi ;;
  'pane send-keys') : > "$HERDR_ENTER_MARK"; printf '{"result":{}}' ;;
  'pane send-text') printf '{"result":{}}' ;;
  'status --json') printf '{"server":{"running":true}}' ;;
  *) exit 99 ;;
esac
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINCAN_HERDR", executable)
	t.Setenv("HERDR_LOG", log)
	t.Setenv("HERDR_ENTER_MARK", filepath.Join(t.TempDir(), "entered"))
	t.Setenv("HERDR_DOORBELL", checkDoorbell)
	return state, log
}

func pendingCheckRecord(t *testing.T, state string) string {
	t.Helper()
	inbox := filepath.Join(state, "task-1.inbox")
	if err := os.Mkdir(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(inbox, "001.msg")
	if err := os.WriteFile(record, []byte("schema=fm-task-inbox.v1\nat=2026-01-01T00:00:00Z\n--\nhello"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(record, old, old); err != nil {
		t.Fatal(err)
	}
	return inbox
}

func callCheck(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestCheckCLIOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, mode, action, reason, delivery string
		record, budget                       bool
		wantAttempts                         string
		wantKeys                             int
	}{
		{name: "quiet with missing Herdr", mode: "missing-binary", action: "quiet"},
		{name: "busy wait", mode: "busy", action: "wait", record: true},
		{name: "existing doorbell", mode: "old", action: "ring", delivery: "sent", record: true, wantAttempts: "001.msg\t1\t", wantKeys: 1},
		{name: "empty composer", mode: "empty", action: "ring", delivery: "deferred", record: true, wantAttempts: "001.msg\t1\t"},
		{name: "missing pane", mode: "missing", action: "escalate", reason: "unavailable", record: true},
		{name: "busy spent budget", mode: "busy", action: "wait", record: true, budget: true, wantAttempts: "001.msg\t3\t0\n"},
		{name: "idle spent budget", mode: "empty", action: "escalate", reason: "budget", record: true, budget: true, wantAttempts: "001.msg\t3\t0\n"},
		{name: "missing Herdr", mode: "missing-binary", action: "ring", delivery: "failed", record: true, wantAttempts: "001.msg\t1\t"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, log := checkCLIFixture(t)
			t.Setenv("HERDR_MODE", test.mode)
			if test.mode == "missing-binary" {
				t.Setenv("TINCAN_HERDR", filepath.Join(t.TempDir(), "absent-herdr"))
			}
			inbox := filepath.Join(state, "task-1.inbox")
			if test.record {
				inbox = pendingCheckRecord(t, state)
			}
			if test.budget {
				if err := os.WriteFile(filepath.Join(inbox, ".ring-state"), []byte("001.msg\t3\t0\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, errText := callCheck("--state", state, "--grace-seconds", "0", "task-1")
			if code != 0 || !strings.Contains(out, "action: "+test.action+"\n") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
			}
			if test.reason != "" && !strings.Contains(out, "reason: "+test.reason+"\n") {
				t.Fatalf("reason output=%q", out)
			}
			if test.delivery != "" && !strings.Contains(out, "delivery: "+test.delivery+"\n") {
				t.Fatalf("delivery output=%q", out)
			}
			if test.record && test.action != "quiet" &&
				!strings.Contains(out, "record: 001.msg\n") {
				t.Fatalf("record output=%q", out)
			}
			if test.mode == "missing-binary" && test.action == "ring" &&
				!strings.Contains(errText, "absent-herdr") {
				t.Fatalf("diagnostic=%q", errText)
			}
			stateBytes, stateErr := os.ReadFile(filepath.Join(inbox, ".ring-state"))
			if test.wantAttempts == "" {
				if !os.IsNotExist(stateErr) {
					t.Fatalf("attempt state=%q err=%v", stateBytes, stateErr)
				}
			} else if stateErr != nil || !strings.HasPrefix(string(stateBytes), test.wantAttempts) {
				t.Fatalf("attempt state=%q err=%v", stateBytes, stateErr)
			}
			if _, err := os.Stat(filepath.Join(inbox, ".escalated")); !os.IsNotExist(err) {
				t.Fatalf("marker written without publisher: %v", err)
			}
			calls, err := os.ReadFile(log)
			if test.action == "quiet" || test.mode == "missing-binary" {
				if !os.IsNotExist(err) {
					t.Fatalf("unexpected Herdr log=%q err=%v", calls, err)
				}
			} else {
				if err != nil || strings.Count(string(calls), "send-keys") != test.wantKeys || strings.Contains(string(calls), "send-text") {
					t.Fatalf("Herdr calls=%q err=%v", calls, err)
				}
				for _, call := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
					if !strings.HasPrefix(call, "session=crew args=") || !strings.HasSuffix(call, " --session crew") {
						t.Fatalf("unscoped call=%q", call)
					}
				}
			}
		})
	}
}

func TestCheckCLIDeferredAttemptReachesBudget(t *testing.T) {
	state, log := checkCLIFixture(t)
	inbox := pendingCheckRecord(t, state)
	args := []string{"--state", state, "--grace-seconds", "0", "--max-attempts", "1", "task-1"}
	code, out, errText := callCheck(args...)
	if code != 0 || !strings.Contains(out, "action: ring\n") ||
		!strings.Contains(out, "delivery: deferred\n") {
		t.Fatalf("first: %d %q %q", code, out, errText)
	}
	code, out, errText = callCheck(args...)
	if code != 0 || !strings.Contains(out, "action: escalate\n") ||
		!strings.Contains(out, "reason: budget\n") ||
		!strings.Contains(out, "attempts: 1\n") {
		t.Fatalf("second: %d %q %q", code, out, errText)
	}
	if _, err := os.Stat(filepath.Join(inbox, ".escalated")); !os.IsNotExist(err) {
		t.Fatalf("marker written without publication: %v", err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(calls), "send-text") ||
		strings.Contains(string(calls), "send-keys") {
		t.Fatalf("unsafe calls=%q err=%v", calls, err)
	}
}

func TestCheckCLIUsageAndOperationalErrors(t *testing.T) {
	for _, test := range []struct {
		name            string
		args            []string
		invalidMetadata bool
		want            int
	}{
		{"relative state", []string{"--state", "relative", "task-1"}, false, 2},
		{"invalid task", []string{"task/1"}, false, 2},
		{"invalid grace", []string{"--grace-seconds", "bad", "task-1"}, false, 2},
		{"negative grace", []string{"--grace-seconds", "-1", "task-1"}, false, 2},
		{"invalid max", []string{"--max-attempts", "bad", "task-1"}, false, 2},
		{"bad metadata", []string{"task-1"}, true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, log := checkCLIFixture(t)
			if test.invalidMetadata {
				pendingCheckRecord(t, state)
				if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte("bad"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{"--state", state}, test.args...)
			code, _, _ := callCheck(args...)
			if code != test.want {
				t.Fatalf("exit=%d want=%d", code, test.want)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("Herdr ran: %v", err)
			}
		})
	}
}

func TestCheckCLIPolicyEnvironmentAndOverrides(t *testing.T) {
	state, _ := checkCLIFixture(t)
	pendingCheckRecord(t, state)
	t.Setenv("FM_TASK_INBOX_GRACE_SECS", "600")
	code, out, errText := callCheck("--state", state, "task-1")
	if code != 0 || out != "action: quiet\n" {
		t.Fatalf("environment grace: %d %q %q", code, out, errText)
	}
	code, out, errText = callCheck("--state", state, "--grace-seconds", "0", "task-1")
	if code != 0 || !strings.Contains(out, "action: ring\n") ||
		!strings.Contains(out, "attempts: 1\n") {
		t.Fatalf("explicit grace: %d %q %q", code, out, errText)
	}
	state, _ = checkCLIFixture(t)
	pendingCheckRecord(t, state)
	t.Setenv("FM_TASK_INBOX_GRACE_SECS", "0")
	t.Setenv("FM_TASK_INBOX_RING_MAX", "0")
	code, out, errText = callCheck("--state", state, "task-1")
	if code != 0 || !strings.Contains(out, "action: escalate\nreason: budget\n") ||
		!strings.Contains(out, "attempts: 0\n") {
		t.Fatalf("zero max from environment: %d %q %q", code, out, errText)
	}
}

func TestCheckCLIHerdrFlagOverridesEnvironment(t *testing.T) {
	state, log := checkCLIFixture(t)
	availableHerdr := os.Getenv("TINCAN_HERDR")
	t.Setenv("TINCAN_HERDR", filepath.Join(t.TempDir(), "missing-herdr"))
	pendingCheckRecord(t, state)
	code, out, errText := callCheck(
		"--state",
		state,
		"--herdr",
		availableHerdr,
		"--grace-seconds",
		"0",
		"task-1",
	)
	if code != 0 || !strings.Contains(out, "action: ring\n") ||
		!strings.Contains(out, "delivery: deferred\n") || errText != "" {
		t.Fatalf("explicit Herdr: %d %q %q", code, out, errText)
	}
	if _, err := os.Stat(log); err != nil {
		t.Fatalf("explicit Herdr did not run: %v", err)
	}
}

func TestCheckCLIKeepsRunResultAfterAcknowledgement(t *testing.T) {
	state, _ := checkCLIFixture(t)
	inbox := pendingCheckRecord(t, state)
	var stdout, stderr bytes.Buffer
	code := runWithCheck(
		[]string{"--state", state, "--grace-seconds", "0", "task-1"},
		&stdout,
		&stderr,
		func(check firstmate.RetryCheck, now time.Time) (firstmate.CheckResult, error) {
			result, err := check.Run(context.Background(), now)
			if err == nil {
				if removeErr := os.Remove(filepath.Join(inbox, "001.msg")); removeErr != nil {
					t.Fatal(removeErr)
				}
			}
			return result, err
		},
	)
	if code != 0 || !strings.Contains(stdout.String(), "action: ring\n") ||
		!strings.Contains(stdout.String(), "attempts: 1\n") ||
		!strings.Contains(stdout.String(), "delivery: deferred\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stateBytes, err := os.ReadFile(filepath.Join(inbox, ".ring-state"))
	if err != nil || !strings.HasPrefix(string(stateBytes), "001.msg\t1\t") {
		t.Fatalf("second evaluation changed attempt state=%q err=%v", stateBytes, err)
	}
}
