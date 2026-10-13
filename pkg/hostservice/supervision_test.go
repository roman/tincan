package hostservice

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/resident"
	"github.com/roman/tincan/pkg/supervise"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestSupervisionSettingsOnlyCompareExplicitFields(t *testing.T) {
	interval := time.Second
	tolerance := 0
	server := &Server{
		config: Config{
			Supervision: supervise.Config{
				Interval:         interval,
				RestartTolerance: 0,
				RestartWindow:    time.Hour,
			},
		},
	}
	if err := server.checkSupervisionSettings(&SupervisionRequest{Settings: &SupervisionSettings{Interval: &interval, RestartTolerance: &tolerance}}); err != nil {
		t.Fatal(err)
	}
	tolerance = 1
	if err := server.checkSupervisionSettings(&SupervisionRequest{Settings: &SupervisionSettings{RestartTolerance: &tolerance}}); err == nil {
		t.Fatal("accepted tolerance mismatch")
	}
}

func TestServeReadyCallbackFailureJoinsControllers(t *testing.T) {
	root := t.TempDir()
	if err := taskstate.Initialize(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("concierge")
	executable, _ := os.Executable()
	var diagnostics bytes.Buffer
	server, err := New(
		Config{
			Root:            root,
			MessageRoot:     filepath.Join(root, "messages"),
			Recipient:       recipient,
			HerdrExecutable: executable,
			Diagnostics:     &diagnostics,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("", "concierge-ready-")
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
	cleaned := make(chan struct{})
	expected := errors.New("ready output failed")
	err = server.ServeReady(context.Background(), listener, func() error {
		_, err := server.controllers.start(
			context.Background(),
			controllerKey{kind: taskController, id: "task-1"},
			controllerEndpoint{session: "work", pane: "pane-1"},
			"binding",
			func(context.Context) (preparedController, error) {
				return preparedController{
					Run:   func(ctx context.Context, ready chan<- struct{}) error { close(ready); <-ctx.Done(); return nil },
					Close: func() error { close(cleaned); return nil },
				}, nil
			},
		)
		if err != nil {
			return err
		}
		return expected
	})
	if !errors.Is(err, expected) {
		t.Fatalf("ready callback error: %v", err)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("ServeReady returned before cleanup")
	}
}

func TestTaskControllerPreparationRejectsChangedEndpoint(t *testing.T) {
	server, key, _ := taskEndpointFixture(t)
	endpoint := controllerEndpoint{session: "work", pane: "old-pane"}
	calls := filepath.Join(t.TempDir(), "calls")
	if err := os.WriteFile(server.config.HerdrExecutable, []byte("#!/bin/sh\nprintf called > '"+calls+"'\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := server.controllers.start(
		context.Background(),
		key,
		endpoint,
		"captured",
		func(ctx context.Context) (preparedController, error) {
			return server.prepareTaskController(ctx, "task-1", endpoint)
		},
	)
	if !errors.Is(err, taskstate.ErrInvalidTransition) {
		t.Fatalf("stale endpoint preparation: %v", err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("stale preparation reached Herdr: %v", err)
	}
	lease, err := resident.AcquireTaskLease(server.config.Root, "task-1")
	if err != nil {
		t.Fatalf("stale preparation retained lease: %v", err)
	}
	lease.Close()
	release, err := server.controllers.acquire(endpoint)
	if err != nil {
		t.Fatalf("stale preparation retained endpoint: %v", err)
	}
	release()
}
