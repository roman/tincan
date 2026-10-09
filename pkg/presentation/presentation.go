// Package presentation serializes Herdr pane presentation changes with Firstmate.
package presentation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

const presentationNamespace = "/tmp/firstmate-herdr-presentation"

// WithLock serializes a named session's presentation changes with Firstmate.
func WithLock(
	ctx context.Context, client herdr.Client, session string, operation func() error,
) error {
	return withLock(ctx, client, session, presentationNamespace, operation)
}

func withLock(
	ctx context.Context, client herdr.Client, session, namespace string, operation func() error,
) (result error) {
	if operation == nil {
		return herdr.ErrResponse
	}
	return withLockSocket(
		ctx,
		client,
		session,
		namespace,
		func(string) error { return operation() },
	)
}

func withLockSocket(
	ctx context.Context, client herdr.Client, session, namespace string,
	operation func(string) error,
) (result error) {
	if _, err := herdr.ParseTarget(session, "validation-pane"); err != nil ||
		operation == nil || client.SessionRunner == nil {
		return herdr.ErrResponse
	}
	socket, err := sessionSocket(ctx, client, session)
	if err != nil {
		return err
	}
	if err := securePresentationNamespace(namespace); err != nil {
		return err
	}
	key := sha256.Sum256([]byte(session + "\x00" + socket))
	path := filepath.Join(namespace, "order-"+hex.EncodeToString(key[:])[:32]+".lock")
	timeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	lock, err := firstmate.AcquirePathLock(path, timeout)
	if err != nil {
		return fmt.Errorf("%w: presentation lock: %v", herdr.ErrCommand, err)
	}
	defer func() { result = errors.Join(result, lock.Release()) }()
	currentSocket, err := sessionSocket(ctx, client, session)
	if err != nil || currentSocket != socket {
		return fmt.Errorf("%w: Herdr session socket changed", herdr.ErrCommand)
	}
	return operation(socket)
}

func securePresentationNamespace(path string) error {
	err := os.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%w: unsafe presentation lock directory", herdr.ErrCommand)
	}
	return nil
}

func sessionSocket(ctx context.Context, client herdr.Client, session string) (string, error) {
	stdout, stderr, err := client.SessionRunner.RunSession(
		ctx,
		session,
		"session",
		"list",
		"--json",
		"--session",
		session,
	)
	if err != nil || len(stderr) > 0 {
		return "", fmt.Errorf("%w: Herdr session list unavailable", herdr.ErrCommand)
	}
	var response struct {
		Sessions []struct {
			Name       string `json:"name"`
			Running    bool   `json:"running"`
			SocketPath string `json:"socket_path"`
		} `json:"sessions"`
	}
	if json.Unmarshal(stdout, &response) != nil {
		return "", herdr.ErrResponse
	}
	var socket string
	count := 0
	for _, candidate := range response.Sessions {
		if candidate.Name == session && candidate.Running {
			count++
			socket = candidate.SocketPath
		}
	}
	if count != 1 || !filepath.IsAbs(socket) {
		return "", fmt.Errorf("%w: named Herdr session is ambiguous", herdr.ErrResponse)
	}
	parent := filepath.Dir(socket)
	resolved, err := filepath.EvalSymlinks(parent)
	if err == nil {
		socket = filepath.Join(resolved, filepath.Base(socket))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: Herdr socket parent: %v", herdr.ErrCommand, err)
	}
	return socket, nil
}
