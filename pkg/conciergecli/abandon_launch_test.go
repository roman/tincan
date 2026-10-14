package conciergecli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskstate"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbandonLaunchRequiresHostAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			root := shortTaskRoot(t)
			seedViewTask(t, root, "task-1", taskstate.Queued, nil, nil)
			if mode == "unauthorized" {
				serveTaskRoot(t, root)
			}
			path := filepath.Join(root, "records", "task-1.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			code := Run(
				context.Background(),
				[]string{"--root", root, "task", "abandon", "task-1", "--generation", "old-gen"},
				nil,
				&output,
				io.Discard,
			)
			if code != 1 {
				t.Fatalf("unadmitted abandonment code %d: %s", code, &output)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("unadmitted abandonment changed record: %v", err)
			}
		})
	}
}

func TestHistoricalAbandonmentReportsCurrentState(t *testing.T) {
	root := lifecycleRoot(t)
	store, err := taskstate.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.Read(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := task.Snapshot()
	snapshot.State = taskstate.InFlight
	receipt := taskstate.LaunchAbandonment{
		Generation: "abandoned-gen",
		Worktree:   "/worktree",
		Branch:     "task-1",
	}
	snapshot.AbandonedLaunches = []taskstate.LaunchAbandonment{receipt}
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: filepath.Join(root, "host.sock"), Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		connection, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		var request hostservice.Request
		if err := json.NewDecoder(connection).Decode(&request); err != nil {
			done <- err
			return
		}
		done <- json.NewEncoder(connection).Encode(hostservice.Response{Outcome: hostservice.Completed, Transition: &snapshot, Abandoned: &receipt})
	}()
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "task", "abandon", "task-1", "--generation", receipt.Generation},
		nil,
		&output,
		io.Discard,
	)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("abandon code %d: %s", code, &output)
	}
	for _, part := range []string{"state: in_flight", "retained_worktree: \"/worktree\"", "retained_branch: \"task-1\"", "inspect current ownership"} {
		if !strings.Contains(output.String(), part) {
			t.Fatalf("missing %q: %s", part, &output)
		}
	}
}
