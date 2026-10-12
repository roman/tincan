package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
)

func TestWorkerReplySurvivesPrimaryRestart(t *testing.T) {
	tasksAXI, err := exec.LookPath("tasks-axi")
	if err != nil {
		t.Fatal("tasks-axi is required for the backlog lifecycle")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Dir(workingDirectory)
	temporaryRoot := t.TempDir()
	commandDirectory := filepath.Join(temporaryRoot, "commands")
	if err := os.Mkdir(commandDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	packages := []string{
		"concierge-backlog", "concierge-task", "tincan-task-send",
		"tincan-task-receive", "tincan-task-reply", "concierge-worker",
	}
	buildArguments := []string{"build", "-buildvcs=false", "-o", commandDirectory}
	for _, name := range packages {
		buildArguments = append(buildArguments, "./cmd/"+name)
	}
	build := exec.Command("go", buildArguments...)
	build.Dir = repositoryRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build commands: %v: %s", err, output)
	}
	fakeDirectory := filepath.Join(temporaryRoot, "fake-bin")
	if err := os.Mkdir(fakeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	fakeHerdr := fmt.Sprintf("#!%s\nexec %q -test.run=^TestFakeHerdr$ -- \"$@\"\n",
		bash, os.Args[0])
	if err := os.WriteFile(filepath.Join(fakeDirectory, "herdr"), []byte(fakeHerdr), 0o700); err != nil {
		t.Fatal(err)
	}
	tasksWrapper := `#!/bin/sh
if [ "$TINCAN_FAIL_DISPOSITION" = 1 ] && [ "$1" = done ]; then
  "$TINCAN_REAL_TASKS_AXI" "$@" || exit $?
  mv "$TINCAN_DISPOSITIONS" "$TINCAN_DISPOSITIONS.hold" || exit $?
  : > "$TINCAN_DISPOSITIONS" || exit $?
  exit 0
fi
exec "$TINCAN_REAL_TASKS_AXI" "$@"
`
	if err := os.WriteFile(filepath.Join(fakeDirectory, "tasks-axi"), []byte(tasksWrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(temporaryRoot, "project")
	worktree := filepath.Join(project, "worktree")
	root := filepath.Join(temporaryRoot, ".concierge")
	messages := filepath.Join(temporaryRoot, ".tincan")
	for _, directory := range []string{worktree, root, messages} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	commandEnvironment := append(os.Environ(),
		"PATH="+fakeDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TINCAN_FAKE_HERDR=1", "TINCAN_FAKE_WORKTREE="+worktree,
		"TINCAN_FAKE_PANE_GONE="+filepath.Join(temporaryRoot, "pane-gone"),
		"TINCAN_REAL_TASKS_AXI="+tasksAXI,
		"TINCAN_DISPOSITIONS="+filepath.Join(messages, "inbox", "firstmate", "dispositions"),
	)
	run := func(name string, arguments ...string) string {
		t.Helper()
		command := exec.Command(filepath.Join(commandDirectory, name), arguments...)
		command.Env = commandEnvironment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", name, arguments, err, output)
		}
		return string(output)
	}
	run("concierge-backlog", "--root", root, "add", "task-1", "Test worker reply",
		"--kind", "task", "--repo", "exmachina")
	run("concierge-task", "--root", root, "adopt", "task-1", "--session", "work",
		"--pane", "pane-1", "--name", "worker-1", "--project", project,
		"--worktree", worktree, "--agent-arg", "resume", "--agent-arg", "thread-1")
	run("concierge-backlog", "--root", root, "start", "task-1")
	generation := outputField(t, run("concierge-worker", "--root", root, "show", "task-1"),
		"spawn_generation")
	if generation == "" {
		t.Fatal("adoption did not publish a worker generation")
	}
	assignmentFile := filepath.Join(temporaryRoot, "assignment.txt")
	if err := os.WriteFile(assignmentFile, []byte("Complete this task"), 0o600); err != nil {
		t.Fatal(err)
	}
	assignment := run("tincan-task-send", "--task-root", root,
		"--message-root", messages, "task-1", "--key", "first-assignment",
		"--body-file", assignmentFile)
	assignmentID := outputField(t, assignment, "id")
	if !strings.Contains(assignment, "notification: sent") {
		t.Fatalf("worker was not notified: %s", assignment)
	}
	received := run("tincan-task-receive", "--task-root", root,
		"--message-root", messages, "read", "task-1", assignmentID,
		"--generation", generation)
	if !strings.Contains(received, "status: current") ||
		!strings.Contains(received, "Complete this task") {
		t.Fatalf("worker did not receive the assignment: %s", received)
	}
	replyFile := filepath.Join(temporaryRoot, "reply.txt")
	if err := os.WriteFile(replyFile, []byte("Task completed"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply := run("tincan-task-reply", "--task-root", root,
		"--message-root", messages, "send", "task-1",
		"--generation", generation, "--key", "completion",
		"--assignment-id", assignmentID, "--body-file", replyFile)
	replyID := outputField(t, reply, "id")
	store, err := inbox.New(messages)
	if err != nil {
		t.Fatal(err)
	}
	worker, _ := inbox.ParseRecipient("task-1")
	if pending, err := store.List(worker); err != nil || len(pending) != 0 {
		t.Fatalf("assignment remained pending after reply: %v, %v", pending, err)
	}
	primary, _ := inbox.ParseRecipient("firstmate")
	replyMessageID, err := inbox.ParseMessageID(replyID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(primary, replyMessageID); err != nil {
		t.Fatalf("reply was not durable before primary restart: %v", err)
	}
	// Replace the disposition directory after tasks-axi stages the Done row.
	// The first resolver then exits with a durable intent and backlog result.
	resolveArguments := []string{"--root", root, "resolve", replyID,
		"--message-root", messages, "--mode", "close"}
	firstResolve := exec.Command(
		filepath.Join(commandDirectory, "concierge-backlog"),
		resolveArguments...)
	firstResolve.Env = append(commandEnvironment, "TINCAN_FAIL_DISPOSITION=1")
	if output, err := firstResolve.CombinedOutput(); err == nil {
		t.Fatalf("first resolve unexpectedly succeeded: %s", output)
	}
	dispositionDirectory := filepath.Join(messages, "inbox", "firstmate", "dispositions")
	restoreDispositions := func() error {
		if err := os.Remove(dispositionDirectory); err != nil {
			return err
		}
		return os.Rename(dispositionDirectory+".hold", dispositionDirectory)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(dispositionDirectory + ".hold"); err == nil {
			_ = restoreDispositions()
		}
	})
	backlogAfterFailure := run("concierge-backlog", "--root", root, "list", "--state", "done")
	if strings.Count(backlogAfterFailure, "task-1,done") != 1 {
		t.Fatalf("first resolve did not finish the task: %s", backlogAfterFailure)
	}
	if err := restoreDispositions(); err != nil {
		t.Fatal(err)
	}
	started, err := store.ReadDisposition(primary, replyMessageID)
	if err != nil || started.State != inbox.DispositionStarted {
		t.Fatalf("first resolve did not record its intent: %+v, %v", started, err)
	}
	for attempt := range 2 {
		resolved := run("concierge-backlog", resolveArguments...)
		if !strings.Contains(resolved, "state: done") {
			t.Fatalf("resolve attempt %d: %s", attempt, resolved)
		}
	}
	disposition, err := store.ReadDisposition(primary, replyMessageID)
	if err != nil || disposition.State != inbox.DispositionComplete {
		t.Fatalf("disposition after restart: %+v, %v", disposition, err)
	}
	if _, err := store.ReadHandled(primary, replyMessageID); err != nil {
		t.Fatalf("reply was not acknowledged after resolution: %v", err)
	}
	backlog := run("concierge-backlog", "--root", root, "list", "--state", "done")
	if strings.Count(backlog, "task-1,done") != 1 {
		t.Fatalf("task did not finish once: %s", backlog)
	}
	stopped := run("concierge-task", "--root", root, "stop", "task-1")
	if !strings.Contains(stopped, "state: pane_closed") {
		t.Fatalf("worker pane did not close: %s", stopped)
	}
	cleaned := run("concierge-backlog", "--root", root, "cleanup", "task-1")
	if !strings.Contains(cleaned, "state: cleaned") {
		t.Fatalf("worker was not retired: %s", cleaned)
	}
	if workers := run("concierge-worker", "--root", root, "list"); !strings.Contains(
		workers,
		"count: 0",
	) {
		t.Fatalf("worker remains registered: %s", workers)
	}
	if pending := run("concierge-task", "--root", root, "pending"); !strings.Contains(
		pending,
		"pending_completions: 0",
	) {
		t.Fatalf("completion remains pending: %s", pending)
	}
}

func outputField(t *testing.T, output, name string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if value, found := strings.CutPrefix(line, name+": "); found {
			return value
		}
	}
	t.Fatalf("%s missing from output: %s", name, output)
	return ""
}

func TestFakeHerdr(t *testing.T) {
	if os.Getenv("TINCAN_FAKE_HERDR") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 || len(os.Args) < separator+3 {
		t.Fatal("fake Herdr command is missing")
	}
	arguments := os.Args[separator+1:]
	commandName := strings.Join(arguments[:2], " ")
	worktree := os.Getenv("TINCAN_FAKE_WORKTREE")
	paneGone := os.Getenv("TINCAN_FAKE_PANE_GONE")
	if os.Getenv("HERDR_SESSION") != "work" || worktree == "" {
		t.Fatal("fake Herdr received the wrong session or worktree")
	}
	switch commandName {
	case "session list":
		fmt.Printf(
			`{"sessions":[{"name":"work","running":true,"socket_path":%q}]}`+"\n",
			filepath.Join(worktree, "herdr.sock"),
		)
	case "workspace list":
		fmt.Println(
			`{"result":{"workspaces":[{"workspace_id":"other-workspace","focused":true,"active_tab_id":"other-tab"}]}}`,
		)
	case "tab list":
		fmt.Println(`{"result":{"tabs":[{"tab_id":"other-tab","focused":true}]}}`)
	case "pane get":
		if _, err := os.Stat(paneGone); err == nil {
			fmt.Fprintln(os.Stderr, `{"error":{"code":"pane_not_found"}}`)
			os.Exit(1)
		}
		fmt.Printf(
			`{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":%q,"workspace_id":"workspace-1","tab_id":"tab-1"}}}`+"\n",
			worktree,
		)
	case "pane close":
		if err := os.WriteFile(paneGone, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Println(`{"result":{}}`)
	case "agent get":
		fmt.Println(
			`{"result":{"agent":{"pane_id":"pane-1","agent_status":"idle","agent":"codex"}}}`,
		)
	case "pane process-info":
		fmt.Println(
			`{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}`,
		)
	case "agent prompt":
		fmt.Println(`{"result":{}}`)
	default:
		t.Fatalf("unexpected fake Herdr command: %v", arguments)
	}
	os.Exit(0)
}
