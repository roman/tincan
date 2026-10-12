package conciergecli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/stateversion"
	"github.com/roman/tincan/pkg/taskstate"
)

func shortTaskRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "concierge-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return taskRoot(t, root)
}

func TestIntakeNeverFallsBackToLocalWrites(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			root := shortTaskRoot(t)
			if mode == "unauthorized" {
				serveTaskRoot(t, root)
			}
			t.Setenv("CONCIERGE_WORKER", "1")
			t.Setenv("CONCIERGE_SPAWN_GENERATION", "inherited-value")
			var output bytes.Buffer
			code := Run(
				context.Background(),
				[]string{
					"--root",
					root,
					"backlog",
					"add",
					"task-1",
					"Work",
					"--kind",
					"ship",
					"--repo",
					"tincan",
				},
				nil,
				&output,
				io.Discard,
			)
			if code != 1 || !strings.Contains(output.String(), "error:") ||
				!strings.Contains(output.String(), "help:") {
				t.Fatalf("code %d output %s", code, &output)
			}
			store, _ := taskstate.NewStore(root)
			if _, err := store.Read(context.Background(), "task-1"); !errors.Is(
				err,
				taskstate.ErrTaskNotFound,
			) {
				t.Fatalf("rejected intake wrote task: %v", err)
			}
		})
	}
}

func serveTaskRoot(t *testing.T, root string) {
	t.Helper()
	recipient, _ := inbox.ParseRecipient("concierge")
	server, err := hostservice.New(root, recipient)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { defer listener.Close(); done <- server.Serve(ctx, listener.UnixListener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("host serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("host did not stop")
		}
	})
}

type readyWriter chan string

func (writer readyWriter) Write(data []byte) (int, error) {
	writer <- string(data)
	return len(data), nil
}

func TestHostServeOwnsOneSocketAndStopsCleanly(t *testing.T) {
	root := shortTaskRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, done := make(readyWriter, 1), make(chan int, 1)
	go func() { done <- Run(ctx, []string{"--root", root, "host", "serve"}, nil, ready, io.Discard) }()
	select {
	case output := <-ready:
		if !strings.Contains(output, "status: ready") {
			t.Fatalf("startup %s", output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host did not become ready")
	}
	var output bytes.Buffer
	if code := Run(context.Background(), []string{"--root", root, "host", "serve"}, nil, &output, io.Discard); code != 1 ||
		!strings.Contains(output.String(), "already") {
		t.Fatalf("duplicate code %d output %s", code, &output)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("shutdown code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("host did not stop")
	}
	if _, err := os.Lstat(filepath.Join(root, "host.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
}

func TestHostRequiresInitializedState(t *testing.T) {
	root, err := os.MkdirTemp("", "concierge-fresh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	if code := Run(ctx, []string{"--root", root, "host", "serve"}, nil, &output, io.Discard); code != 1 {
		t.Fatalf("uninitialized host code %d output %s", code, &output)
	}
	if _, err := os.Stat(filepath.Join(root, ".host.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uninitialized host created lease: %v", err)
	}
	if err := taskstate.Initialize(context.Background(), root); err != nil {
		t.Fatalf("host prevented later initialization: %v", err)
	}
}

func TestHostRetainsMigrationLeaseUntilShutdown(t *testing.T) {
	root := shortTaskRoot(t)
	recipient, _ := inbox.ParseRecipient("concierge")
	server, err := hostservice.New(root, recipient)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	guard, err := stateversion.AcquireMigration(ctx, root)
	if guard != nil {
		guard.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("migration entered active host: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	guard, err = stateversion.AcquireMigration(context.Background(), root)
	if guard != nil {
		guard.Close()
	}
	if !errors.Is(err, stateversion.ErrIncompatible) {
		t.Fatalf("shutdown retained migration lease: %v", err)
	}
}

func TestHostWaitsForMigrationBeforeCreatingRuntimeFiles(t *testing.T) {
	root, err := os.MkdirTemp("", "concierge-migration-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	guard, err := stateversion.AcquireMigration(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	if code := Run(ctx, []string{"--root", root, "host", "serve"}, nil, &output, io.Discard); code != 1 {
		t.Fatalf("migration host code %d output %s", code, &output)
	}
	for _, name := range []string{".host.lock", "host.sock"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("created %s during migration: %v", name, err)
		}
	}
}

func TestHostPreservesNonSocketOccupants(t *testing.T) {
	for _, kind := range []string{"regular", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := shortTaskRoot(t)
			path := filepath.Join(root, "host.sock")
			target := filepath.Join(root, "precious")
			if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "regular" {
				if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			var output bytes.Buffer
			if code := Run(ctx, []string{"--root", root, "host", "serve"}, nil, &output, io.Discard); code != 1 {
				t.Fatalf("occupied socket code %d output %s", code, &output)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if (info.Mode()&os.ModeSymlink != 0) != (kind == "symlink") {
				t.Fatalf("occupant changed: %v", info.Mode())
			}
			contents, err := os.ReadFile(path)
			if err != nil || string(contents) != "preserve" {
				t.Fatalf("occupant changed: %q %v", contents, err)
			}
		})
	}
}

func TestHostRebindsAbandonedSocket(t *testing.T) {
	root := shortTaskRoot(t)
	path := filepath.Join(root, "host.sock")
	abandoned, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	abandoned.SetUnlinkOnClose(false)
	if err := abandoned.Close(); err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("concierge")
	server, err := hostservice.New(root, recipient)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := server.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("replacement socket unavailable: %v", err)
	}
	connection.Close()
}
