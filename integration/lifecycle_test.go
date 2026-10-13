package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/internal/nativetest"
	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestWorkerReplyResolutionSurvivesClientRestart(t *testing.T) {
	if nativetest.RunUnderParent(t) {
		return
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Dir(workingDirectory)
	temporaryRoot, err := os.MkdirTemp("", "concierge-integration-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(temporaryRoot)
	commandDirectory := filepath.Join(temporaryRoot, "commands")
	if err := os.Mkdir(commandDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	buildArguments := []string{
		"build",
		"-buildvcs=false",
		"-o",
		commandDirectory,
		"./cmd/concierge",
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
		"CONCIERGE_SPAWN_GENERATION=gen-1",
	)
	t.Setenv("TINCAN_FAKE_HERDR", "1")
	t.Setenv("TINCAN_FAKE_WORKTREE", worktree)
	t.Setenv("TINCAN_FAKE_PANE_GONE", filepath.Join(temporaryRoot, "pane-gone"))
	run := func(arguments ...string) string {
		t.Helper()
		command := exec.Command(filepath.Join(commandDirectory, "concierge"), arguments...)
		command.Env = commandEnvironment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("concierge %v: %v: %s", arguments, err, output)
		}
		return string(output)
	}
	run("--root", root, "state", "init")
	claim := exec.Command(
		filepath.Join(commandDirectory, "concierge"),
		"--root",
		root,
		"primary",
		"claim",
	)
	payload, _ := json.Marshal(
		map[string]string{
			"cwd":             temporaryRoot,
			"session_id":      "integration-primary",
			"hook_event_name": "SessionStart",
		},
	)
	claim.Stdin = bytes.NewReader(payload)
	claim.Env = commandEnvironment
	if output, err := claim.CombinedOutput(); err != nil {
		t.Fatalf("claim %v: %s", err, output)
	}
	recipient, _ := inbox.ParseRecipient("concierge")
	host, err := hostservice.New(
		hostservice.Config{
			Root:            root,
			MessageRoot:     messages,
			Recipient:       recipient,
			HerdrExecutable: filepath.Join(fakeDirectory, "herdr"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := host.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hostContext, stopHost := context.WithCancel(context.Background())
	hostDone := make(chan error, 1)
	go func() { defer listener.Close(); hostDone <- host.Serve(hostContext, listener.UnixListener) }()
	defer func() {
		stopHost()
		select {
		case err := <-hostDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("host did not stop")
		}
	}()

	service, err := taskops.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), taskstate.Definition{ID: "task-1", Title: "Test worker reply", Kind: "task", Repo: "exmachina"}); err != nil {
		t.Fatal(err)
	}
	run(
		"--root",
		root,
		"task",
		"adopt",
		"task-1",
		"--session",
		"work",
		"--pane",
		"pane-1",
		"--name",
		"worker-1",
		"--project",
		project,
		"--worktree",
		worktree,
		"--generation",
		"gen-1",
		"--agent-arg",
		"resume",
		"--agent-arg",
		"thread-1",
	)
	if _, err := service.Start(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
	generation := outputField(t, run("--root", root, "worker", "show", "task-1"),
		"spawn_generation")
	if generation == "" {
		t.Fatal("adoption did not publish a worker generation")
	}
	assignmentFile := filepath.Join(temporaryRoot, "assignment.txt")
	if err := os.WriteFile(assignmentFile, []byte("Complete this task"), 0o600); err != nil {
		t.Fatal(err)
	}
	assignment := run(
		"--root",
		root,
		"--message-root",
		messages,
		"task",
		"send",
		"task-1",
		"--key",
		"first-assignment",
		"--body-file",
		assignmentFile,
	)
	assignmentID := outputField(t, assignment, "message_id")
	if !strings.Contains(assignment, "notification: sent") {
		t.Fatalf("worker was not notified: %s", assignment)
	}
	received := run(
		"--root",
		root,
		"--message-root",
		messages,
		"task",
		"receive",
		"task-1",
		assignmentID,
		"--generation",
		generation,
	)
	if !strings.Contains(received, "status: current") ||
		!strings.Contains(received, "Complete this task") {
		t.Fatalf("worker did not receive the assignment: %s", received)
	}
	replyFile := filepath.Join(temporaryRoot, "reply.txt")
	if err := os.WriteFile(replyFile, []byte("Task completed"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply := run("--root", root, "--message-root", messages, "task", "reply", "send", "task-1",
		"--generation", generation, "--key", "completion",
		"--assignment-id", assignmentID, "--body-file", replyFile)
	replyID := outputField(t, reply, "message_id")
	store, err := inbox.New(messages)
	if err != nil {
		t.Fatal(err)
	}
	worker, _ := inbox.ParseRecipient("task-1")
	if pending, err := store.List(worker); err != nil || len(pending) != 0 {
		t.Fatalf("assignment remained pending after reply: %v, %v", pending, err)
	}
	primary, _ := inbox.ParseRecipient("concierge")
	replyMessageID, err := inbox.ParseMessageID(replyID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(primary, replyMessageID); err != nil {
		t.Fatalf("reply was not durable before client restart: %v", err)
	}
	// Keep the durable result and disposition intent across separate CLI processes.
	if _, _, err := store.BeginDisposition(primary, replyMessageID,
		"finish:task-1:"+generation+":close:2026-10-12"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Finish(context.Background(), "task-1", generation, taskstate.Close, "2026-10-12"); err != nil {
		t.Fatal(err)
	}
	resolveArguments := []string{"--root", root, "--message-root", messages,
		"backlog", "resolve", replyID, "--closed-on", "2026-10-12"}
	started, err := store.ReadDisposition(primary, replyMessageID)
	if err != nil || started.State != inbox.DispositionStarted {
		t.Fatalf("resolution intent was not durable: %+v, %v", started, err)
	}
	for attempt := range 2 {
		resolved := run(resolveArguments...)
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
	backlog := run("--root", root, "backlog", "list", "--state", "done")
	if strings.Count(backlog, "\"task-1\",done") != 1 {
		t.Fatalf("task did not finish once: %s", backlog)
	}
	run("--root", root, "task", "stop", "task-1", "--generation", generation)
	if _, err := os.Stat(filepath.Join(temporaryRoot, "pane-gone")); err != nil {
		t.Fatalf("worker pane did not close: %v", err)
	}
	run("--root", root, "backlog", "cleanup", "task-1", "--generation", generation)
	if workers := run("--root", root, "worker", "list"); !strings.Contains(
		workers,
		"count: 0",
	) {
		t.Fatalf("worker remains registered: %s", workers)
	}
	if pending := run("--root", root, "task", "pending"); !strings.Contains(
		pending,
		"count: 0",
	) {
		t.Fatalf("completion remains pending: %s", pending)
	}
}

func outputField(t *testing.T, output, name string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if value, found := strings.CutPrefix(line, name+": "); found {
			return strings.Trim(value, "\"")
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
