package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

func seedViewTask(
	t *testing.T,
	root, id string,
	state taskstate.State,
	worker *taskstate.Worker,
	completion *taskstate.Completion,
) {
	t.Helper()
	task, err := taskstate.NewMigrated(
		taskstate.Snapshot{
			Definition: taskstate.Definition{ID: id, Title: "Work", Repo: "repo", Kind: "audit"},
			State:      state,
			Worker:     worker,
			Completion: completion,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := task.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "records", id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func viewRoot(t *testing.T) string {
	t.Helper()
	root := taskRoot(t, t.TempDir())
	worker := taskstate.Worker{
		Name:          "worker",
		Harness:       "codex",
		Session:       "work",
		Pane:          "pane-1",
		Workspace:     "workspace-1",
		Tab:           "tab:1",
		Project:       "/repo",
		Worktree:      "/worktree",
		Generation:    "gen-1",
		PendingLaunch: "gen-1",
		AgentArgs:     []string{"resume", "thread-1"},
	}
	seedViewTask(t, root, "bound", taskstate.InFlight, &worker, nil)
	worker.Name = "adopting"
	worker.Pane = "pane-2"
	worker.PendingLaunch = ""
	worker.PendingAdoption = true
	worker.Workspace = ""
	worker.Tab = ""
	seedViewTask(t, root, "adopting", taskstate.Queued, &worker, nil)
	seedViewTask(
		t,
		root,
		"retired",
		taskstate.InFlight,
		nil,
		&taskstate.Completion{
			Generation: "gen-2",
			Mode:       taskstate.Retain,
			Phase:      taskstate.Retired,
		},
	)
	seedViewTask(t, root, "queued", taskstate.Queued, nil, nil)
	return root
}

func TestWorkerAndTaskCommandsSelectRecordViews(t *testing.T) {
	root := viewRoot(t)
	for _, trial := range []struct {
		args []string
		want []string
	}{
		{[]string{"worker", "list"}, []string{"count: 1\nworkers[1]", "pending_adoptions[1]"}},
		{[]string{"worker", "show", "bound"}, []string{"spawn_generation: gen-1\n", "pending_launch: gen-1\n", "kind: codex\n"}},
		{[]string{"worker", "show", "adopting"}, []string{"adoption: pending\n"}},
		{[]string{"task", "list"}, []string{"count: 1\ntasks[1]", "\"bound\",\"audit\",\"/worktree\""}},
		{[]string{"task", "pending"}, []string{"count: 1\npending_completions[1]", "\"retired\",\"retain\",\"gen-2\",\"retired\""}},
	} {
		args := append(
			[]string{"--message-root", "relative", "--recipient", "../invalid"},
			trial.args...)
		got := invoke(t, root, "", args...)
		for _, want := range trial.want {
			if !strings.Contains(got, want) {
				t.Fatalf("%v missing %q: %s", trial.args, want, got)
			}
		}
	}
}

func TestDefaultBriefUsesSelectedMailboxAndDoesNotChangeRecords(t *testing.T) {
	root := viewRoot(t)
	messageRoot := t.TempDir()
	store, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("operator")
	message, err := store.Send(recipient, "wake", "Selected wake")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := inbox.ParseRecipient("concierge")
	if _, err := store.Send(other, "other", "Unselected wake"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "records", "bound.json")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--message-root", messageRoot, "--recipient", "operator"}
	defaultBrief := invoke(t, root, "", args...)
	explicitBrief := invoke(t, root, "", append(args, "brief")...)
	if defaultBrief != explicitBrief {
		t.Fatalf("default/explicit brief differ:\n%s\n%s", defaultBrief, explicitBrief)
	}
	limited := invoke(t, root, "", append(args, "brief", "--limit", "1")...)
	for _, want := range []string{"wakes: 1 pending for operator\n", "Selected wake", "backlog: 1 of 2 queued\n", "workers: 1 registered\n", "pending_completions: 1 of 1\n"} {
		if !strings.Contains(limited, want) {
			t.Fatalf("missing %q in %s", want, limited)
		}
	}
	if strings.Contains(limited, "Unselected wake") {
		t.Fatal("brief read another primary recipient")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("brief published task: %v", err)
	}
	messages, err := store.List(recipient)
	if err != nil || len(messages) != 1 || messages[0] != message {
		t.Fatalf("brief changed pending messages: %v %v", messages, err)
	}
}

func TestViewUsageDoesNotFallThroughToBrief(t *testing.T) {
	root := viewRoot(t)
	for _, args := range [][]string{{"worker"}, {"task"}, {"unknown"}, {"brief", "--typo"}, {"brief", "--limit", "0"}, {"brief", "--limit", "101"}, {"--recipient", "../invalid", "brief"}, {"--message-root", "relative", "brief"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), append([]string{"--root", root}, args...), strings.NewReader(""), &stdout, &stderr); code != 2 ||
			!strings.Contains(stdout.String(), "error:") ||
			strings.Contains(stdout.String(), "wakes:") ||
			stderr.Len() != 0 {
			t.Fatalf("%v code%d stdout%s stderr%s", args, code, &stdout, &stderr)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--root", root, "worker", "show", "queued"}, strings.NewReader(""), &stdout, &stderr); code != 1 ||
		!strings.Contains(stdout.String(), "no worker record") {
		t.Fatalf("missing worker: %d %s", code, &stdout)
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestViewCommandsPropagateOutputFailures(t *testing.T) {
	root := viewRoot(t)
	for _, args := range [][]string{{"worker", "list"}, {"worker", "show", "bound"}, {"task", "list"}, {"task", "pending"}, {"brief"}} {
		if code := Run(context.Background(), append([]string{"--root", root, "--message-root", t.TempDir()}, args...), strings.NewReader(""), failedOutput{}, io.Discard); code != 1 {
			t.Fatalf("%v write failure exit mismatch", args)
		}
	}
}

func TestBriefMessageRootPrecedenceAndDefaultRecipient(t *testing.T) {
	for _, source := range []string{"explicit", "environment", "directory"} {
		t.Run(source, func(t *testing.T) {
			directory := t.TempDir()
			t.Chdir(directory)
			t.Setenv("TINCAN_HOME", "")
			roots := map[string]string{
				"explicit":    t.TempDir(),
				"environment": t.TempDir(),
				"directory":   filepath.Join(directory, ".tincan"),
			}
			recipient, _ := inbox.ParseRecipient("concierge")
			for name, path := range roots {
				messages, err := inbox.New(path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := messages.Send(recipient, name, "Wake from "+name); err != nil {
					t.Fatal(err)
				}
			}
			var args []string
			if source != "directory" {
				t.Setenv("TINCAN_HOME", roots["environment"])
			}
			if source == "explicit" {
				args = []string{"--message-root", roots[source]}
			}
			got := invoke(t, viewRoot(t), "", args...)
			if !strings.Contains(got, "wakes: 1 pending for concierge\n") ||
				!strings.Contains(got, "Wake from "+source) {
				t.Fatalf("%s selection: %s", source, got)
			}
			for name := range roots {
				if name != source && strings.Contains(got, "Wake from "+name) {
					t.Fatalf("read %s instead of %s: %s", name, source, got)
				}
			}
		})
	}
}
