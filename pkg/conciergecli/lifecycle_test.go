package conciergecli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
)

func lifecycleRoot(t *testing.T) string {
	t.Helper()
	root := shortTaskRoot(t)
	worker := taskstate.Worker{
		Name:       "worker",
		Harness:    "codex",
		Session:    "work",
		Pane:       "pane-1",
		Workspace:  "workspace-1",
		Tab:        "tab-1",
		Project:    "/repo",
		Worktree:   "/worktree",
		Generation: "gen-1",
		AgentArgs:  []string{"resume", "thread-1"},
	}
	seedViewTask(t, root, "task-1", taskstate.Queued, &worker, nil)
	return root
}

func expectLifecycleError(t *testing.T, root string, code int, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	got := Run(
		context.Background(),
		append([]string{"--root", root}, args...),
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if got != code || !strings.Contains(stdout.String(), "error:") || stderr.Len() != 0 {
		t.Fatalf("%v: code%d stdout%s stderr%s", args, got, &stdout, &stderr)
	}
	return stdout.String()
}

func TestBacklogLifecycleCloseAndRetain(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	for _, mode := range []string{"close", "retain"} {
		t.Run(mode, func(t *testing.T) {
			root := lifecycleRoot(t)
			claimNativeParent(t, root)
			directory := t.TempDir()
			script := `#!/bin/sh
[ "$HERDR_SESSION" = work ] || exit 71
[ "$*" = 'pane get pane-1 --session work' ] || exit 72
printf '%s\n' '{"error":{"code":"pane_not_found"}}'
`
			if err := os.WriteFile(filepath.Join(directory, "herdr"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory)
			t.Setenv("HERDR_SESSION", "wrong-ambient-session")
			recipient, _ := inbox.ParseRecipient("concierge")
			serveConfiguredTaskRoot(
				t,
				hostservice.Config{
					Root:            root,
					MessageRoot:     filepath.Join(root, "messages"),
					Recipient:       recipient,
					HerdrExecutable: filepath.Join(directory, "herdr"),
				},
			)
			invoke(
				t,
				root,
				"",
				"backlog",
				"hold",
				"task-1",
				"--reason",
				"Review",
				"--kind",
				"captain",
				"--until",
				"2026-10-15",
			)
			expectLifecycleError(t, root, 1, "backlog", "start", "task-1")
			for range 2 {
				invoke(t, root, "", "backlog", "release", "task-1")
			}
			for range 2 {
				if got := invoke(t, root, "", "backlog", "start", "task-1"); got != "task: \"task-1\"\nstate: in_flight\n" {
					t.Fatalf("start output: %s", got)
				}
			}
			finish := []string{"backlog", "finish", "task-1", "--generation", "gen-1"}
			state := "done"
			if mode == "retain" {
				invoke(t, root, "", "backlog", "hold", "task-1", "--reason", "More work")
				finish = append(finish, "--mode", "retain")
				state = "queued"
			} else {
				finish = append(finish, "--closed-on", "2026-10-12")
			}
			wrong := append([]string{}, finish...)
			wrong[4] = "wrong-generation"
			expectLifecycleError(t, root, 1, wrong...)
			for range 2 {
				if got := invoke(t, root, "", finish...); got != "task: \"task-1\"\nstate: "+state+"\ngeneration: \"gen-1\"\n" {
					t.Fatalf("finish: %s", got)
				}
			}
			expectLifecycleError(
				t,
				root,
				1,
				"backlog",
				"cleanup",
				"task-1",
				"--generation",
				"wrong-generation",
			)
			for range 2 {
				if got := invoke(t, root, "", "backlog", "cleanup", "task-1", "--generation", "gen-1"); got != "task: \"task-1\"\nstate: "+state+"\ngeneration: \"gen-1\"\n" {
					t.Fatalf("cleanup output: %s", got)
				}
			}
			store, _ := taskstate.NewStore(root)
			task, err := store.Read(context.Background(), "task-1")
			if err != nil || task.Snapshot().Worker != nil ||
				!task.HasReceipt("gen-1", taskstate.CompletionMode(mode)) {
				t.Fatalf("cleanup: %#v %v", task.Snapshot(), err)
			}
			if mode == "retain" {
				invoke(t, root, "", "backlog", "release", "task-1")
			}
		})
	}
}

func TestFinishDatesFailBeforeRootDiscovery(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("CONCIERGE_HOME", "")
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{nil, {"--closed-on", "2026-02-30"}, {"--closed-on", "2026-1-2"}, {"--mode", "retain", "--closed-on", "2026-10-12"}} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"backlog", "finish", "task-1", "--generation", "gen-1"}, flags...)
		if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 2 ||
			!strings.Contains(stdout.String(), "--closed-on") ||
			strings.Contains(stdout.String(), "--root") ||
			stderr.Len() != 0 {
			t.Fatalf("%v: %d %s %s", args, code, &stdout, &stderr)
		}
	}
	for _, command := range []string{"start", "finish", "cleanup", "hold", "release"} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), []string{"backlog", command, "--help"}, strings.NewReader(""), &stdout, &stderr); code != 0 ||
			stdout.Len() == 0 {
			t.Fatalf("%s help: %d %s", command, code, &stdout)
		}
	}
}

func TestCleanupRequiresPaneDeparture(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	root := lifecycleRoot(t)
	claimNativeParent(t, root)
	directory := t.TempDir()
	recipient, _ := inbox.ParseRecipient("concierge")
	serveConfiguredTaskRoot(
		t,
		hostservice.Config{
			Root:            root,
			MessageRoot:     filepath.Join(root, "messages"),
			Recipient:       recipient,
			HerdrExecutable: filepath.Join(directory, "herdr"),
		},
	)
	invoke(t, root, "", "backlog", "start", "task-1")
	invoke(
		t,
		root,
		"",
		"backlog",
		"finish",
		"task-1",
		"--generation",
		"gen-1",
		"--closed-on",
		"2026-10-12",
	)
	if err := os.WriteFile(filepath.Join(directory, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' '{\"result\":{\"pane\":{\"pane_id\":\"pane-1\"}}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	expectLifecycleError(t, root, 1, "backlog", "cleanup", "task-1", "--generation", "gen-1")
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil || task.Snapshot().Worker == nil || task.Snapshot().Completion == nil {
		t.Fatalf("premature retirement: %#v %v", task.Snapshot(), err)
	}
}

func startFixtureTask(t *testing.T, root string) {
	t.Helper()
	service, err := taskops.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Start(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
}

func holdFixtureTask(t *testing.T, root, reason string) {
	t.Helper()
	service, err := taskops.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PlaceHold(context.Background(), "task-1", taskstate.Hold{Reason: reason}); err != nil {
		t.Fatal(err)
	}
}
