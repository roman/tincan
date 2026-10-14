package conciergecli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestReleaseWorktreeReportsVerifiedHostReceipt(t *testing.T) {
	root := shortTaskRoot(t)
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: filepath.Join(root, "host.sock"), Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	received := make(chan hostservice.Request, 1)
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
		received <- request
		snapshot := taskstate.Snapshot{
			Definition: taskstate.Definition{
				ID:    "task-1",
				Title: "Work",
				Repo:  "repo",
				Kind:  "ship",
			},
			State: taskstate.Queued,
			Receipt: &taskstate.Receipt{
				Generation: "gen-1",
				Mode:       taskstate.Retain,
				Lease: taskstate.WorktreeLease{
					ID:         "lease-1",
					Holder:     "holder",
					Project:    "/repo",
					Path:       "/worker",
					Branch:     "task/work",
					BaseCommit: strings.Repeat("a", 40),
					State:      taskstate.LeaseReturned,
				},
			},
		}
		done <- json.NewEncoder(connection).Encode(hostservice.Response{Outcome: hostservice.Completed, Transition: &snapshot})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output bytes.Buffer
	code := Run(
		ctx,
		[]string{"--root", root, "backlog", "release-worktree", "task-1", "--generation", "gen-1"},
		nil,
		&output,
		io.Discard,
	)
	if code != 0 {
		t.Fatalf("release code=%d: %s", code, &output)
	}
	for _, part := range []string{"generation: \"gen-1\"", "lease: returned", "worktree: \"/worker\"", "branch: \"task/work\""} {
		if !strings.Contains(output.String(), part) {
			t.Fatalf("release output lacks %q: %s", part, &output)
		}
	}
	select {
	case request := <-received:
		if request.Operation != hostservice.ReleaseWorktree ||
			request.Transition.TaskID != "task-1" ||
			request.Transition.Generation != "gen-1" {
			t.Fatalf("release request: %+v", request)
		}
	case <-ctx.Done():
		t.Fatal("release request missing")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
