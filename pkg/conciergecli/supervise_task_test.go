package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/resident"
	"github.com/roman/tincan/pkg/taskstate"
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
  printf '%s\n' '{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":"codex"}}}' ;;
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
		"../unused",
		"supervise",
		"--interval",
		"1h",
		"task",
		"task-1",
	}
}

func waitForResidentLog(
	t *testing.T,
	ctx context.Context,
	result <-chan int,
	record string,
	ready func(string) bool,
) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		calls, _ := os.ReadFile(record)
		if ready(string(calls)) {
			return
		}
		select {
		case code := <-result:
			t.Fatalf("resident ended before event: %d calls=%s", code, calls)
		case <-ctx.Done():
			t.Fatalf("resident event timed out: %s", calls)
		case <-ticker.C:
		}
	}
}

type residentOutput struct {
	bytes.Buffer
	reported chan struct{}
}

func (output *residentOutput) Write(value []byte) (int, error) {
	count, err := output.Buffer.Write(value)
	if bytes.Contains(value, []byte("delivery_error:")) {
		select {
		case output.reported <- struct{}{}:
		default:
		}
	}
	return count, err
}

func TestSuperviseTaskUsesBoundDoorbellAndReleasesLease(t *testing.T) {
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
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			output := residentOutput{reported: make(chan struct{}, 1)}
			result := make(chan int, 1)
			go func() {
				result <- Run(ctx, taskSupervisionArgs(root, messageRoot), strings.NewReader(""), &output, io.Discard)
			}()
			waitForResidentLog(t, ctx, result, record, func(calls string) bool {
				return strings.Contains(calls, "work|agent prompt pane-1 ") &&
					strings.Contains(calls, "work|pane process-info")
			})
			if mode == "notify failure" {
				select {
				case <-output.reported:
				case code := <-result:
					t.Fatalf("resident ended before delivery report: %d %s", code, &output)
				case <-ctx.Done():
					t.Fatal("delivery failure was not reported")
				}
			}
			cancel()
			select {
			case code := <-result:
				if code != 0 {
					t.Fatalf("cancellation: %d %s", code, &output)
				}
			case <-time.After(time.Second):
				t.Fatal("task resident ignored cancellation")
			}
			if mode == "notify failure" {
				line := strings.TrimSuffix(output.String(), "\n")
				value, err := strconv.Unquote(strings.TrimPrefix(line, "delivery_error: "))
				if !strings.HasPrefix(line, "delivery_error: ") || strings.Contains(line, "\n") ||
					!strings.HasSuffix(output.String(), "\n") ||
					err != nil ||
					value == "" {
					t.Fatalf("invalid delivery report: %q %v", output.String(), err)
				}
			}
			if mode != "notify failure" && output.Len() != 0 {
				t.Fatalf("success output: %s", &output)
			}
			calls, _ := os.ReadFile(record)
			for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if !strings.HasPrefix(line, "work|") {
					t.Fatalf("wrong session: %s", calls)
				}
			}
			if mode == "restart" {
				if !strings.Contains(
					string(calls),
					"agent start worker --kind codex --pane pane-1 -- resume thread-1",
				) {
					t.Fatalf("restart arguments: %s", calls)
				}
				store, _ := taskstate.NewStore(root)
				task, err := store.Read(context.Background(), "task-1")
				if err != nil || task.Snapshot().Worker.Generation == "gen-1" ||
					task.Snapshot().Worker.PendingLaunch != "" {
					t.Fatalf("restart incomplete: %#v %v", task.Snapshot(), err)
				}
			}
			lease, err := resident.AcquireTaskLease(root, "task-1")
			if err != nil {
				t.Fatalf("lease remains held: %v", err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSuperviseTaskRejectsInvalidConfigAndDuplicateController(t *testing.T) {
	root, messageRoot, record := taskSupervisionFixture(t)
	args := taskSupervisionArgs(root, messageRoot)
	expectLifecycleError(t, root, 2, append(args[2:], "--interval", "0s")...)
	if _, err := os.Stat(filepath.Join(root, "task-1.controller.lock")); !os.IsNotExist(err) {
		t.Fatalf("invalid config created lease: %v", err)
	}
	lease, err := resident.AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	expectLifecycleError(t, root, 1, args[2:]...)
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("second controller contacted Herdr: %v", err)
	}
}

func TestSuperviseTaskInitialFailurePreventsWatcher(t *testing.T) {
	root, messageRoot, _ := taskSupervisionFixture(t)
	t.Setenv("TEST_ADOPTION_FAIL", "1")
	expectLifecycleError(t, root, 1, taskSupervisionArgs(root, messageRoot)[2:]...)
	if _, err := os.Stat(filepath.Join(messageRoot, "inbox", "task-1", "pending")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("watcher started after failed check: %v", err)
	}
	lease, err := resident.AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatalf("failed check retained lease: %v", err)
	}
	lease.Close()
}

func TestSuperviseTaskCancellationDuringInitialCheck(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	root, messageRoot, record := taskSupervisionFixture(t)
	t.Setenv("TEST_SLEEP", sleep)
	t.Setenv("TEST_RESIDENT_BLOCK", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var output bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- Run(ctx, taskSupervisionArgs(root, messageRoot), strings.NewReader(""), &output, io.Discard)
	}()
	waitForResidentLog(
		t,
		ctx,
		result,
		record,
		func(calls string) bool { return strings.Contains(calls, "pane get pane-1") },
	)
	cancel()
	select {
	case code := <-result:
		if code != 0 || output.Len() != 0 {
			t.Fatalf("initial cancel: %d %s", code, &output)
		}
	case <-time.After(time.Second):
		t.Fatal("initial check ignored cancellation")
	}
	lease, err := resident.AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatalf("initial cancellation retained lease: %v", err)
	}
	lease.Close()
}

func TestSuperviseTaskStopsWhenDeliveryReportCannotBeWritten(t *testing.T) {
	root, messageRoot, _ := taskSupervisionFixture(t)
	t.Setenv("TEST_RESIDENT_NOTIFYFAIL", "1")
	messages, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("task-1")
	if _, err := messages.Send(recipient, "work", "Work"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan int, 1)
	go func() {
		result <- Run(ctx, taskSupervisionArgs(root, messageRoot), strings.NewReader(""), failedOutput{}, io.Discard)
	}()
	select {
	case code := <-result:
		if code != 1 {
			t.Fatalf("delivery report writer: %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("failed delivery report did not stop supervision")
	}
	lease, err := resident.AcquireTaskLease(root, "task-1")
	if err != nil {
		t.Fatalf("report failure retained lease: %v", err)
	}
	lease.Close()
}
