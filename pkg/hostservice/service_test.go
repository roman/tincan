package hostservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/primary"
	"github.com/roman/tincan/pkg/taskstate"
)

func fixture(t *testing.T, configure func(*Server)) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	if err := taskstate.Initialize(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("concierge")
	executable := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	server, err := New(
		Config{
			Root:            root,
			MessageRoot:     filepath.Join(root, "messages"),
			Recipient:       recipient,
			HerdrExecutable: executable,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if configure != nil {
		configure(server)
	}
	dir, err := os.MkdirTemp("", "concierge-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "host.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
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
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("host startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("host startup timed out")
	}
	return server, root, socket
}

func intakeRequest() Request {
	return Request{Operation: Intake, Intake: &taskstate.Definition{
		ID: "task-1", Title: "Work", Repo: "exmachina", Kind: "ship", Body: "Task body",
	}}
}

func TestUnauthorizedIntakeWritesNoTask(t *testing.T) {
	_, root, socket := fixture(t, nil)
	response, err := Call(context.Background(), socket, intakeRequest())
	if err != nil || response.Outcome != Unauthorized {
		t.Fatalf("response %+v, error %v", response, err)
	}
	store, _ := taskstate.NewStore(root)
	if _, err := store.Read(context.Background(), "task-1"); !errors.Is(
		err,
		taskstate.ErrTaskNotFound,
	) {
		t.Fatalf("unauthorized request wrote task: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "backlog.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unauthorized request wrote view: %v", err)
	}
}

func TestIntakeUsesKernelPeerAndRetriesIdentically(t *testing.T) {
	peers := make(chan primary.Caller, 2)
	_, root, socket := fixture(t, func(server *Server) {
		server.authorize = func(caller primary.Caller) error { peers <- caller; return nil }
	})
	for range 2 {
		response, err := Call(context.Background(), socket, intakeRequest())
		if err != nil || response.Outcome != Completed || response.Task == nil ||
			response.Task.Definition.ID != "task-1" {
			t.Fatalf("response %+v, error %v", response, err)
		}
		peer := <-peers
		if peer.PID != os.Getpid() || peer.UID != uint32(os.Geteuid()) {
			t.Fatalf("peer %+v", peer)
		}
	}
	store, _ := taskstate.NewStore(root)
	tasks, err := store.List(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].Definition().Body != "Task body" {
		t.Fatalf("tasks %+v, error %v", tasks, err)
	}
}

func TestRejectsInvalidRequestsBeforeDispatch(t *testing.T) {
	for _, input := range []string{
		`{"operation":"unknown"}`,
		`{"operation":"intake"}`,
		`{"operation":"intake","intake":{"id":"task-1"},"caller_pid":1}`,
		`{"operation":"intake","intake":{"id":"task-1","extra":true}}`,
		`{"operation":"intake","intake":{}} {}`,
		`{"operation":"intake","intake":{"body":"` + strings.Repeat("x", maxRequestBytes) + `"}}`,
	} {
		t.Run(input[:min(len(input), 70)], func(t *testing.T) {
			_, _, socket := fixture(t, func(server *Server) {
				server.authorize = func(primary.Caller) error { t.Error("invalid request reached admission"); return nil }
			})
			conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = conn.Write([]byte(input))
			_ = conn.CloseWrite()
			var response Response
			if err := json.NewDecoder(conn).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if response.Outcome != Invalid {
				t.Fatalf("response %+v", response)
			}
		})
	}
}

func TestIntakePreservesPublicationUncertainty(t *testing.T) {
	task, err := taskstate.NewQueued(*intakeRequest().Intake)
	if err != nil {
		t.Fatal(err)
	}
	_, _, socket := fixture(t, func(server *Server) {
		server.authorize = func(primary.Caller) error { return nil }
		server.create = func(context.Context, taskstate.Definition) (taskstate.Task, error) {
			return task, errors.Join(taskstate.ErrPublicationUnconfirmed, errors.New("sync failed"))
		}
	})
	response, err := Call(context.Background(), socket, intakeRequest())
	if err != nil || response.Outcome != Uncertain || response.Task == nil ||
		!strings.Contains(response.Error, "sync failed") {
		t.Fatalf("response %+v, error %v", response, err)
	}
}

func TestIntakeRetryAfterLostResponse(t *testing.T) {
	created, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	_, root, socket := fixture(t, func(server *Server) {
		server.authorize = func(primary.Caller) error { return nil }
		create := server.create
		server.create = func(ctx context.Context, definition taskstate.Definition) (taskstate.Task, error) {
			task, err := create(ctx, definition)
			if calls.Add(1) == 1 {
				close(created)
				<-release
			}
			return task, err
		}
	})
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if err := json.NewEncoder(conn).Encode(intakeRequest()); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-created:
	case <-time.After(5 * time.Second):
		t.Fatal("intake did not finish")
	}
	_ = conn.Close()
	close(release)
	response, err := Call(context.Background(), socket, intakeRequest())
	if err != nil || response.Outcome != Completed {
		t.Fatalf("retry response %+v, error %v", response, err)
	}
	store, _ := taskstate.NewStore(root)
	tasks, err := store.List(context.Background())
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks %+v, error %v", tasks, err)
	}
}

func TestCallReportsUnknownResponses(t *testing.T) {
	for _, response := range []string{
		"", `{}`, `{"outcome":"unknown"}`, `{"outcome":"completed"}`, `{"outcome":"failed"}`,
		`{"outcome":"completed","task":{"definition":{"id":"task-1","title":"Different","repo":"exmachina","kind":"ship","body":"Task body"},"state":"queued"}}`,
		`{"outcome":"completed","error":"failed","task":{"definition":{"id":"task-1","title":"Work","repo":"exmachina","kind":"ship","body":"Task body"},"state":"queued"}}`,
	} {
		t.Run(response, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "concierge-lost-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "host.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
				_, _ = io.WriteString(conn, response)
			}()
			_, err = Call(context.Background(), socket, intakeRequest())
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("call error %v", err)
			}
			<-done
		})
	}
}

func TestServerBoundsConnectionsAndStopsWithIdleClients(t *testing.T) {
	var connections []*net.UnixConn
	t.Cleanup(func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
	_, _, socket := fixture(t, nil)
	for range maxConnections {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var data [1]byte
	if _, err := conn.Read(data[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("excess connection error %v", err)
	}
}

func TestIntakeReturnsBusyWithoutWaitingForClaimLock(t *testing.T) {
	var lock *os.File
	t.Cleanup(func() {
		if lock != nil {
			_ = lock.Close()
		}
	})
	_, root, socket := fixture(t, nil)
	var err error
	lock, err = os.OpenFile(filepath.Join(root, "primary.claim.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := Call(ctx, socket, intakeRequest())
	if err != nil || response.Outcome != Busy {
		t.Fatalf("response %+v, error %v", response, err)
	}
	store, _ := taskstate.NewStore(root)
	if _, err := store.Read(context.Background(), "task-1"); !errors.Is(
		err,
		taskstate.ErrTaskNotFound,
	) {
		t.Fatalf("busy request wrote task: %v", err)
	}
}
