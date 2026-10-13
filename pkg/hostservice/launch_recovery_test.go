package hostservice

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/taskstate"
)

func TestStartupReconcilesUncertainPaneWithoutResubmission(t *testing.T) {
	server, input := launchRequestFixture(t)
	server.config.Diagnostics = io.Discard
	ctx := context.Background()
	task, err := server.reserveInitialLaunch(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	generation := task.Snapshot().LaunchIntent.Generation
	if _, err := server.store.Update(ctx, input.TaskID, func(task taskstate.Task) (taskstate.Task, error) { return task.BeginLaunchPreparation(generation) }); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.Update(ctx, input.TaskID, func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchPane(generation) }); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\ncase \"$3\" in\ncheck-ref-format) exit 0 ;;\ncat-file) printf 'commit\\n'; exit 0 ;;\nesac\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server.config.GitExecutable = git
	finds, effects := filepath.Join(t.TempDir(), "finds"), filepath.Join(t.TempDir(), "effects")
	t.Setenv("RECOVERY_FINDS", finds)
	t.Setenv("RECOVERY_EFFECTS", effects)
	script := `#!/bin/sh
case "$1 $2" in
'tab list') printf 'find\n' >> "$RECOVERY_FINDS"; printf '%s\n' '{"result":{"tabs":[]}}' ;;
'tab create'|'agent start') printf effect > "$RECOVERY_EFFECTS"; exit 72 ;;
*) exit 72 ;;
esac
`
	if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := server.restoreTaskControllers(ctx); err != nil {
		t.Fatal(err)
	}
	server.reconcileInitialTasks(ctx)
	data, err := os.ReadFile(finds)
	if err != nil || strings.Count(string(data), "find\n") != 2 {
		t.Fatalf("startup and background discovery: %s, %v", data, err)
	}
	if _, err := os.Stat(effects); !os.IsNotExist(err) {
		t.Fatal("uncertain pane creation was resubmitted")
	}
	task, err = server.store.Read(ctx, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	intent := task.Snapshot().LaunchIntent
	if intent == nil || intent.Generation != generation ||
		intent.Phase != taskstate.LaunchPaneRequested ||
		intent.Endpoint != nil {
		t.Fatal("recovery replaced unresolved launch state")
	}
	if _, err := server.supervision(ctx, StopSupervision, SupervisionRequest{TaskID: input.TaskID}); err != nil {
		t.Fatal(err)
	}
	server.reconcileInitialTasks(ctx)
	data, _ = os.ReadFile(finds)
	if strings.Count(string(data), "find\n") != 2 {
		t.Fatal("periodic recovery undid pending launch stop")
	}
	if _, err := server.launch(ctx, input); err == nil {
		t.Fatal("missing uncertain pane reported launch success")
	}
	data, _ = os.ReadFile(finds)
	if strings.Count(string(data), "find\n") != 3 {
		t.Fatal("explicit retry did not resume discovery after stop")
	}
}

func TestUncertainLaunchReservationRemainsTrackedForRecovery(t *testing.T) {
	server, input := launchRequestFixture(t)
	git := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\ncase \"$3\" in\ncheck-ref-format) exit 0 ;;\ncat-file) printf 'commit\\n'; exit 0 ;;\nesac\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server.config.GitExecutable = git
	server.reserveLaunchIntent = func(ctx context.Context, taskID string, intent taskstate.LaunchIntent) (taskstate.Task, error) {
		task, err := server.store.ReserveInitialLaunch(ctx, taskID, intent)
		if err != nil {
			return task, err
		}
		return task, taskstate.ErrPublicationUnconfirmed
	}
	ctx := context.Background()
	if _, err := server.launch(ctx, input); !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		t.Fatalf("uncertain reservation: %v", err)
	}
	before, err := server.store.Read(ctx, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	intent := before.Snapshot().LaunchIntent
	if intent == nil || intent.Phase != taskstate.LaunchReserved || intent.Endpoint != nil ||
		before.Snapshot().Worker != nil {
		t.Fatal("uncertain reservation advanced to launch effects")
	}
	if len(server.controllers.initialResidentTasks()) != 1 {
		t.Fatal("uncertain reservation lost recovery admission")
	}
	server.reserveLaunchIntent = server.store.ReserveInitialLaunch
	server.reconcileInitialTasks(ctx)
	after, err := server.store.Read(ctx, input.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	recovered := after.Snapshot().LaunchIntent
	if recovered == nil || recovered.Phase != taskstate.LaunchPreparing {
		t.Fatal("background recovery did not confirm and continue the stored reservation")
	}
	copy := *recovered
	copy.Phase = intent.Phase
	if !reflect.DeepEqual(*intent, copy) {
		t.Fatal("recovery changed the reserved launch identity or request")
	}
}

func TestRecoveredLaunchReservationRejectsStoppedSelection(t *testing.T) {
	registry := newControllerRegistry(context.Background())
	defer registry.close()
	key := controllerKey{kind: taskController, id: "task-1"}
	if err := registry.awaitInitialResident(key); err != nil {
		t.Fatal(err)
	}
	selected := registry.initialResidentTasks()[0]
	if err := registry.stop(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.reserveRecoveredLaunch(selected); !errors.Is(
		err,
		errInitialResidentStopped,
	) {
		t.Fatalf("stopped continuation: %v", err)
	}
	if err := registry.awaitInitialResident(key); err != nil {
		t.Fatal(err)
	}
	release, err := registry.reserveRecoveredLaunch(key)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func initialRecoveryFixture(t *testing.T) (*Server, controllerKey, string) {
	t.Helper()
	server, key, _ := taskEndpointFixture(t)
	ctx := context.Background()
	directory := t.TempDir()
	task, _ := server.store.Read(ctx, key.id)
	snapshot := task.Snapshot()
	snapshot.Worker.Project, snapshot.Worker.Worktree = directory, directory
	task, err := taskstate.NewMigrated(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := task.Encode()
	if err := os.WriteFile(filepath.Join(server.config.Root, "records", key.id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECOVERY_WORKTREE", directory)
	failure := filepath.Join(t.TempDir(), "failure")
	if err := os.WriteFile(failure, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECOVERY_FAILURE", failure)
	script := `#!/bin/sh
[ -e "$RECOVERY_FAILURE" ] && exit 72
case "$1 $2" in
'pane get') printf '{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":"%s"}}}\n' "$RECOVERY_WORKTREE" ;;
'agent get') printf '%s\n' '{"result":{"agent":{"pane_id":"pane-1","agent_status":"idle","agent":"codex"}}}' ;;
'pane process-info') printf '%s\n' '{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}' ;;
*) exit 72 ;;
esac
`
	if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	server.config.Diagnostics = io.Discard
	return server, key, failure
}

func TestInitialReconciliationRetriesRegistrationAndPreservesStop(t *testing.T) {
	server, key, failure := initialRecoveryFixture(t)
	ctx := context.Background()
	if err := server.controllers.awaitInitialResident(key); err != nil {
		t.Fatal(err)
	}
	server.reconcileInitialTasks(ctx)
	if len(server.controllers.initialResidentTasks()) != 1 {
		t.Fatal("transient registration failure lost recovery tracking")
	}
	if err := os.Remove(failure); err != nil {
		t.Fatal(err)
	}
	server.reconcileInitialTasks(ctx)
	if len(server.controllers.initialResidentTasks()) != 0 {
		t.Fatal("successful registration remains pending")
	}
	server.controllers.mu.Lock()
	entry := server.controllers.entries[key]
	server.controllers.mu.Unlock()
	if entry == nil {
		t.Fatal("reconciliation did not register the resident")
	}
	if _, err := server.supervision(ctx, StopSupervision, SupervisionRequest{TaskID: key.id}); err != nil {
		t.Fatal(err)
	}
	server.reconcileInitialTasks(ctx)
	server.controllers.mu.Lock()
	entry = server.controllers.entries[key]
	server.controllers.mu.Unlock()
	if entry != nil {
		t.Fatal("periodic reconciliation undid an explicit stop")
	}
}

func TestServeRecoversInitialResidentWithoutClientAndJoinsOnCancel(t *testing.T) {
	server, key, failure := initialRecoveryFixture(t)
	server.config.Supervision.Interval = 5 * time.Millisecond
	if err := server.controllers.close(); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("", "initial-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: filepath.Join(directory, "host.sock"), Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- server.ServeReady(ctx, listener, func() error { close(ready); return nil }) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("host did not join recovery on cancellation")
		}
	})
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("host did not become ready after initial registration failure")
	}
	if len(server.controllers.initialResidentTasks()) != 1 {
		t.Fatal("startup did not reconstruct initial registration tracking")
	}
	if err := os.Remove(failure); err != nil {
		t.Fatal(err)
	}
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		server.controllers.mu.Lock()
		entry := server.controllers.entries[key]
		active := entry != nil && entry.active
		server.controllers.mu.Unlock()
		if active {
			return
		}
		select {
		case <-poll.C:
		case <-timeout.C:
			t.Fatal("host did not retry registration without a client request")
		}
	}
}
