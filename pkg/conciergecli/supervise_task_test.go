package conciergecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/resident"
	"github.com/roman/tincan/pkg/supervise"
	"github.com/roman/tincan/pkg/taskstate"
	"io"
	"strconv"
)

func taskSupervisionFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root, messageRoot, _ := assignmentSendFixture(t)
	directory := os.Getenv("PATH")
	record := filepath.Join(directory, "resident-calls")
	script := `#!/bin/sh
[ "$HERDR_SESSION" = work ] || exit 71
printf '%s|%s\n' "$HERDR_SESSION" "$*" >> "$TEST_RESIDENT_CALLS"
[ "$TEST_ADOPTION_FAIL" = 1 ] && exit 72
case "$1 $2" in
 'pane get')
  if [ "$TEST_RESIDENT_BLOCK" = 1 ]; then exec "$TEST_SLEEP" 60; fi
  printf '{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":"%s","workspace_id":"workspace-1","tab_id":"tab-1"}}}\n' "$TEST_WORKTREE" ;;
 'agent get')
  if [ "$TEST_RESIDENT_MISSING" = 1 ] && [ ! -f "$TEST_RESIDENT_STARTED" ]; then printf '%s\n' '{"error":{"code":"agent_not_found"}}'; exit 1; fi
  printf '%s\n' '{"result":{"agent":{"pane_id":"pane-1","agent_status":"idle","agent":"codex"}}}' ;;
 'pane process-info') printf '%s\n' '{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}' ;;
 'agent start') printf started > "$TEST_RESIDENT_STARTED"; printf '%s\n' '{"result":{}}' ;;
 'agent prompt') [ "$TEST_RESIDENT_NOTIFYFAIL" = 1 ] && exit 73; printf '%s\n' '{"result":{}}' ;;
 *) exit 74 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "herdr"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_RESIDENT_CALLS", record)
	t.Setenv("TEST_RESIDENT_STARTED", filepath.Join(directory, "started"))
	t.Setenv("TEST_ADOPTION_FAIL", "")
	t.Setenv("TEST_RESIDENT_MISSING", "")
	t.Setenv("TEST_RESIDENT_NOTIFYFAIL", "")
	t.Setenv("TEST_RESIDENT_BLOCK", "")
	return root, messageRoot, record
}

func taskSupervisionArgs(root, messageRoot string) []string {
	return []string{
		"--root",
		root,
		"--message-root",
		messageRoot,
		"--recipient",
		"concierge",
		"supervise",
		"--interval",
		"1h",
		"task",
		"task-1",
	}
}

func serveTaskSupervision(t *testing.T, root, messageRoot string, diagnostics io.Writer) {
	t.Helper()
	claimNativeParent(t, root)
	recipient, _ := inbox.ParseRecipient("concierge")
	serveConfiguredTaskRoot(
		t,
		hostservice.Config{
			Root:            root,
			MessageRoot:     messageRoot,
			Recipient:       recipient,
			HerdrExecutable: filepath.Join(os.Getenv("PATH"), "herdr"),
			Supervision: supervise.Config{
				Interval:         time.Hour,
				RestartTolerance: 5,
				RestartWindow:    time.Minute,
			},
			Diagnostics: diagnostics,
		},
	)
}

type supervisionDiagnostic chan string

func (diagnostics supervisionDiagnostic) Write(data []byte) (int, error) {
	select {
	case diagnostics <- string(data):
	default:
	}
	return len(data), nil
}

func TestSuperviseTaskUsesBoundDoorbellAndReleasesLease(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	for _, mode := range []string{"running", "restart", "notify failure"} {
		t.Run(mode, func(t *testing.T) {
			root, messageRoot, record := taskSupervisionFixture(t)
			if mode == "restart" {
				t.Setenv("TEST_RESIDENT_MISSING", "1")
			}
			if mode == "notify failure" {
				t.Setenv("TEST_RESIDENT_NOTIFYFAIL", "1")
			}
			messages, _ := inbox.New(messageRoot)
			recipient, _ := inbox.ParseRecipient("task-1")
			if _, err := messages.Send(recipient, "work", "Work"); err != nil {
				t.Fatal(err)
			}
			diagnostics := make(supervisionDiagnostic, 8)
			serveTaskSupervision(t, root, messageRoot, diagnostics)
			args := taskSupervisionArgs(root, messageRoot)[2:]
			want := "task: \"task-1\"\nsupervision: active\n"
			for range 2 {
				if got := invoke(t, root, "", args...); got != want {
					t.Fatalf("start: %s", got)
				}
			}
			if lease, err := resident.AcquireTaskLease(root, "task-1"); err == nil {
				lease.Close()
				t.Fatal("client exit stopped resident")
			}
			var calls []byte
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				calls, _ = os.ReadFile(record)
				if strings.Contains(string(calls), "work|agent prompt pane-1 ") &&
					strings.Contains(string(calls), "work|pane process-info") {
					break
				}
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatalf("missing delivery/health: %s", calls)
				}
			}
			if mode == "notify failure" {
				select {
				case report := <-diagnostics:
					if !strings.Contains(report, "controller_error:") {
						t.Fatal(report)
					}
				case <-time.After(time.Second):
					t.Fatal("missing diagnostic")
				}
			}
			if mode == "restart" {
				store, _ := taskstate.NewStore(root)
				task, err := store.Read(context.Background(), "task-1")
				if err != nil {
					t.Fatal(err)
				}
				worker := task.Snapshot().Worker
				if worker.Generation == "gen-1" || worker.PendingLaunch != "" {
					t.Fatalf("restart incomplete: %#v", worker)
				}
				expected := "agent start worker --kind codex --pane pane-1 -- -c shell_environment_policy.set.CONCIERGE_SPAWN_GENERATION=" + strconv.Quote(
					worker.Generation,
				) + " resume thread-1"
				if !strings.Contains(string(calls), expected) {
					t.Fatalf("restart args: %s", calls)
				}
			}
			if got := invoke(t, root, "", "supervise", "stop", "task", "task-1"); got != "task: \"task-1\"\nsupervision: stopped\n" {
				t.Fatal(got)
			}
			lease, err := resident.AcquireTaskLease(root, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			lease.Close()
		})
	}
}

func TestSuperviseTaskRejectsInvalidConfigAndDuplicateController(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	root, messageRoot, record := taskSupervisionFixture(t)
	args := taskSupervisionArgs(root, messageRoot)[2:]
	expectLifecycleError(t, root, 2, append(args, "--interval", "0s")...)
	lease, err := resident.AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	serveTaskSupervision(t, root, messageRoot, io.Discard)
	expectLifecycleError(t, root, 1, args...)
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("duplicate contacted Herdr: %v", err)
	}
}

func TestSuperviseTaskInitialFailurePreventsWatcher(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	root, messageRoot, _ := taskSupervisionFixture(t)
	t.Setenv("TEST_ADOPTION_FAIL", "1")
	diagnostics := make(supervisionDiagnostic, 8)
	serveTaskSupervision(t, root, messageRoot, diagnostics)
	select {
	case report := <-diagnostics:
		if !strings.Contains(report, "restore task task-1") {
			t.Fatal(report)
		}
	default:
		t.Fatal("missing restore failure")
	}
	// A failed restore leaves the admitted host usable for operator repairs.
	expectLifecycleError(t, root, 1, taskSupervisionArgs(root, messageRoot)[2:]...)
	if _, err := os.Stat(filepath.Join(messageRoot, "inbox", "task-1", "pending")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("watcher after failed health: %v", err)
	}
	lease, err := resident.AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if got := invoke(t, root, "", "supervise", "stop", "task", "task-1"); !strings.Contains(
		got,
		"stopped",
	) {
		t.Fatal(got)
	}
}

func TestSuperviseTaskMailboxStartupFailureDoesNotReportActive(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	root, messageRoot, _ := taskSupervisionFixture(t)
	if err := os.WriteFile(filepath.Join(messageRoot, "inbox"), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	diagnostics := make(supervisionDiagnostic, 8)
	serveTaskSupervision(t, root, messageRoot, diagnostics)
	select {
	case <-diagnostics:
	default:
		t.Fatal("missing restoration failure")
	}
	failed := expectLifecycleError(t, root, 1, taskSupervisionArgs(root, messageRoot)[2:]...)
	if strings.Contains(failed, "supervision: active") {
		t.Fatal(failed)
	}
	lease, err := resident.AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
}
