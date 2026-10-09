package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/supervise"
)

type sessionReply struct {
	stdout string
	stderr string
	err    error
}

type sessionCall struct {
	session string
	args    []string
}

type scriptedSessionRunner struct {
	replies []sessionReply
	calls   []sessionCall
	onCall  func([]string)
}

func (runner *scriptedSessionRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, sessionCall{session: session, args: args})
	if runner.onCall != nil {
		runner.onCall(args)
	}
	if len(runner.replies) == 0 {
		panic("unexpected Herdr command")
	}
	reply := runner.replies[0]
	runner.replies = runner.replies[1:]
	return []byte(reply.stdout), []byte(reply.stderr), reply.err
}

type shellOnlyProcesses struct{}

func (shellOnlyProcesses) Snapshot(context.Context) ([]herdr.Process, error) {
	return []herdr.Process{{PID: 100, PPID: 1, Name: "zsh"}}, nil
}

func shellProcessReply() sessionReply {
	return sessionReply{
		stdout: `{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":100,"name":"zsh","argv0":"zsh"}]}}}`,
	}
}

func registeredAgentReply() sessionReply {
	return sessionReply{
		stdout: `{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":"codex"}}}`,
	}
}

func registeredSupervisorTask(t *testing.T) (string, string, *crew.Registry) {
	t.Helper()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "tasks")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := strings.Join([]string{
		"window=work:pane-1", "endpoint_task_id=task-1", "worktree=" + worktree,
		"project=" + root, "harness=codex", "kind=crewmate", "spawn_gen=gen-1",
		"backend=herdr", "herdr_session=work", "herdr_workspace_id=workspace-1",
		"herdr_tab_id=tab-1", "herdr_pane_id=pane-1", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(crew.Record{
		TaskID: "task-1", Name: "worker-1", Kind: "codex", PaneID: "pane-1",
		HerdrSession: "work", Project: root, Worktree: worktree,
		SpawnGeneration: "gen-1", AgentArgs: []string{"resume", "thread-1"},
	}); err != nil {
		t.Fatal(err)
	}
	return root, worktree, registry
}

func paneReply(directory string) sessionReply {
	return sessionReply{
		stdout: fmt.Sprintf(
			`{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":%q}}}`,
			directory,
		),
	}
}

func missingAgent() sessionReply {
	return sessionReply{stderr: `{"error":{"code":"agent_not_found"}}`, err: errors.New("exit 1")}
}

func TestRegisteredWorkerRestartsMissingTaskWorker(t *testing.T) {
	root, worktree, registry := registeredSupervisorTask(t)
	runner := &scriptedSessionRunner{replies: []sessionReply{
		paneReply(worktree), missingAgent(),
		paneReply(worktree), missingAgent(), missingAgent(),
		{stdout: `{"result":{}}`}, registeredAgentReply(),
	}}
	runner.onCall = func(args []string) {
		if len(args) < 2 || args[0] != "agent" || args[1] != "start" {
			return
		}
		data, err := os.ReadFile(filepath.Join(root, "tasks", "task-1.meta"))
		if err != nil || strings.Contains(string(data), "spawn_gen=gen-1\n") {
			t.Fatalf("generation was not published before launch: %q, %v", data, err)
		}
	}
	worker := registeredWorker{
		registry: registry, taskID: "task-1", client: herdr.Client{SessionRunner: runner},
	}
	if err := supervise.Check(context.Background(), worker); !errors.Is(
		err,
		supervise.ErrRestarted,
	) {
		t.Fatalf("check error = %v", err)
	}
	record, err := registry.Get("task-1")
	if err != nil || record.SpawnGeneration == "gen-1" || record.PendingGeneration != "" {
		t.Fatalf("record after restart = %+v, error %v", record, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "tasks", "task-1.meta"))
	if err != nil || !strings.Contains(string(data), "spawn_gen="+record.SpawnGeneration+"\n") {
		t.Fatalf("metadata after restart = %q, error %v", data, err)
	}
	for _, call := range runner.calls {
		if call.session != "work" {
			t.Fatalf("command escaped session: %+v", call)
		}
	}
	launches := 0
	for _, call := range runner.calls {
		if len(call.args) >= 3 &&
			reflect.DeepEqual(call.args[:3], []string{"agent", "start", "worker-1"}) {
			launches++
		}
	}
	if len(runner.replies) != 0 || launches != 1 {
		t.Fatalf("Herdr calls = %+v, unused replies = %d", runner.calls, len(runner.replies))
	}
}

func TestRegisteredWorkerRefusesStaleHerdrRegistration(t *testing.T) {
	_, worktree, registry := registeredSupervisorTask(t)
	runner := &scriptedSessionRunner{replies: []sessionReply{
		paneReply(worktree), registeredAgentReply(),
		paneReply(worktree), registeredAgentReply(), shellProcessReply(),
		paneReply(worktree), registeredAgentReply(),
	}}
	worker := registeredWorker{
		registry: registry, taskID: "task-1",
		client: herdr.Client{SessionRunner: runner, Processes: shellOnlyProcesses{}},
	}
	if err := supervise.Check(context.Background(), worker); !errors.Is(err, supervise.ErrRestart) {
		t.Fatalf("stale registration restart = %v", err)
	}
	record, err := registry.Get("task-1")
	if err != nil || record.SpawnGeneration != "gen-1" || len(runner.replies) != 0 {
		t.Fatalf("record = %+v, error = %v, unused replies = %d", record, err, len(runner.replies))
	}
}

func TestRegisteredWorkerRejectsWrongPaneDirectory(t *testing.T) {
	_, _, registry := registeredSupervisorTask(t)
	runner := &scriptedSessionRunner{replies: []sessionReply{paneReply(t.TempDir())}}
	worker := registeredWorker{
		registry: registry,
		taskID:   "task-1",
		client:   herdr.Client{SessionRunner: runner},
	}
	if err := supervise.Check(context.Background(), worker); !errors.Is(err, supervise.ErrProbe) {
		t.Fatalf("check error = %v", err)
	}
	record, err := registry.Get("task-1")
	if err != nil || record.SpawnGeneration != "gen-1" || len(runner.calls) != 1 {
		t.Fatalf("record = %+v, error = %v, calls = %+v", record, err, runner.calls)
	}
}

func TestRegisteredWorkerDoesNotRotateOccupiedPane(t *testing.T) {
	_, worktree, registry := registeredSupervisorTask(t)
	runner := &scriptedSessionRunner{replies: []sessionReply{
		paneReply(worktree), missingAgent(), paneReply(worktree),
		{
			stdout: `{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":"claude"}}}`,
		},
	}}
	worker := registeredWorker{
		registry: registry,
		taskID:   "task-1",
		client:   herdr.Client{SessionRunner: runner},
	}
	if err := supervise.Check(context.Background(), worker); !errors.Is(err, supervise.ErrRestart) {
		t.Fatalf("check error = %v", err)
	}
	record, err := registry.Get("task-1")
	if err != nil || record.SpawnGeneration != "gen-1" || len(runner.replies) != 0 {
		t.Fatalf("record = %+v, error = %v, unused replies = %d", record, err, len(runner.replies))
	}
}

func TestRegisteredWorkerRejectsStaleTaskBeforeHerdr(t *testing.T) {
	root, _, registry := registeredSupervisorTask(t)
	path := filepath.Join(root, "tasks", "task-1.meta")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "spawn_gen=gen-1", "spawn_gen=gen-2", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedSessionRunner{}
	worker := registeredWorker{
		registry: registry,
		taskID:   "task-1",
		client:   herdr.Client{SessionRunner: runner},
	}
	if err := supervise.Check(context.Background(), worker); !errors.Is(err, supervise.ErrProbe) {
		t.Fatalf("check error = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Herdr was called for stale task: %+v", runner.calls)
	}
}

func TestRegisteredWorkerUsesEndpointHealth(t *testing.T) {
	for _, test := range []struct {
		name        string
		healthAgent sessionReply
		wantMissing bool
	}{
		{
			name:        "alive",
			healthAgent: sessionReply{stdout: `{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":"codex"}}}`},
		},
		{name: "dead", healthAgent: missingAgent(), wantMissing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, worktree, registry := registeredSupervisorTask(t)
			runner := &scriptedSessionRunner{replies: []sessionReply{
				paneReply(worktree),
				{
					stdout: `{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":"codex"}}}`,
				},
				paneReply(worktree), test.healthAgent,
			}}
			if !test.wantMissing {
				runner.replies = append(
					runner.replies,
					sessionReply{
						stdout: `{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":200,"name":"codex","argv0":"codex"}]}}}`,
					},
				)
			}
			worker := registeredWorker{
				registry: registry,
				taskID:   "task-1",
				client:   herdr.Client{SessionRunner: runner},
			}
			err := worker.Observe(context.Background())
			if errors.Is(err, supervise.ErrMissing) != test.wantMissing ||
				(!test.wantMissing && err != nil) {
				t.Fatalf("observation error = %v", err)
			}
			if len(runner.replies) != 0 {
				t.Fatalf("unused Herdr replies: %d", len(runner.replies))
			}
		})
	}
}

func TestTaskModeRejectsMixedLaunchFlags(t *testing.T) {
	root, _, _ := registeredSupervisorTask(t)
	_, err := workerFromCLI(cli{Root: root, Task: "task-1", Pane: "pane-2"}, herdr.Client{})
	if err == nil {
		t.Fatal("accepted task mode with independent pane flag")
	}
	_, err = workerFromCLI(cli{Task: "task-1"}, herdr.Client{})
	if err == nil {
		t.Fatal("accepted task mode without state root")
	}
}
