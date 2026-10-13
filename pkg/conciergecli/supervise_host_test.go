package conciergecli

import (
	"bytes"
	"context"
	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/resident"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupervisionMutationsRequireHostAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		for _, args := range [][]string{
			{"supervise", "task", "task-1"},
			{"supervise", "pane", "--pane", "pane-1", "--name", "worker", "--kind", "codex"},
			{"supervise", "stop", "task", "task-1"},
			{"supervise", "stop", "pane", "--pane", "pane-1"},
		} {
			t.Run(mode+"/"+strings.Join(args, " "), func(t *testing.T) {
				root, messageRoot, record := taskSupervisionFixture(t)
				if mode == "unauthorized" {
					recipient, _ := inbox.ParseRecipient("concierge")
					serveConfiguredTaskRoot(
						t,
						hostservice.Config{
							Root:            root,
							MessageRoot:     messageRoot,
							Recipient:       recipient,
							HerdrExecutable: unusedHerdr(t),
							Session:         os.Getenv("HERDR_SESSION"),
						},
					)
				}
				before, err := os.ReadFile(filepath.Join(root, "records", "task-1.json"))
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				var output bytes.Buffer
				code := Run(
					ctx,
					append([]string{"--root", root, "--message-root", messageRoot}, args...),
					nil,
					&output,
					io.Discard,
				)
				if code != 1 || !strings.Contains(output.String(), "error:") {
					t.Fatalf("unadmitted code %d: %s", code, &output)
				}
				if _, err := os.Stat(record); !os.IsNotExist(err) {
					t.Fatalf("unadmitted Herdr call: %v", err)
				}
				after, _ := os.ReadFile(filepath.Join(root, "records", "task-1.json"))
				if !bytes.Equal(before, after) {
					t.Fatal("unadmitted task mutation")
				}
			})
		}
	}
}

func TestSupervisionStopRejectsMonitorFlagsBeforeStoppingResident(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	root, messageRoot, _ := taskSupervisionFixture(t)
	serveTaskSupervision(t, root, messageRoot, io.Discard)
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "supervise", "--interval", "1h", "stop", "task", "task-1"},
		nil,
		&output,
		io.Discard,
	)
	if code != 2 {
		t.Fatalf("explicit stop settings: %d %s", code, &output)
	}
	lease, err := resident.AcquireTaskLease(root, "task-1")
	if err == nil {
		lease.Close()
		t.Fatal("rejected stop reached resident")
	}
}

func TestSupervisionStopFailureIncludesStopHelp(t *testing.T) {
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", shortTaskRoot(t), "supervise", "stop", "task", "task-1"},
		nil,
		&output,
		io.Discard,
	)
	if code != 1 || !strings.Contains(output.String(), "concierge supervise stop task TASK_ID") {
		t.Fatalf("stop help: %d %s", code, &output)
	}
}

func TestAdoptionRetryPreservesActiveTaskResident(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	root, messageRoot, _ := taskSupervisionFixture(t)
	serveTaskSupervision(t, root, messageRoot, io.Discard)
	args := []string{
		"task",
		"adopt",
		"task-1",
		"--generation",
		"gen-1",
		"--name",
		"worker",
		"--session",
		"work",
		"--pane",
		"pane-1",
		"--project",
		os.Getenv("TEST_WORKTREE"),
		"--worktree",
		os.Getenv("TEST_WORKTREE"),
		"--agent-arg",
		"resume",
		"--agent-arg",
		"thread-1",
	}
	if got := invoke(t, root, "", args...); got != "task: \"task-1\"\nstate: in_flight\ngeneration: \"gen-1\"\n" {
		t.Fatalf("confirmed retry: %s", got)
	}
	if lease, err := resident.AcquireTaskLease(root, "task-1"); err == nil {
		lease.Close()
		t.Fatal("adoption retry stopped resident")
	}

	mismatch := append([]string{}, args...)
	mismatch[4] = "different-generation"
	expectLifecycleError(t, root, 1, mismatch...)
	if lease, err := resident.AcquireTaskLease(root, "task-1"); err == nil {
		lease.Close()
		t.Fatal("mismatched retry stopped resident")
	}
}
