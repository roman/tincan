package presentation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/herdr"
)

func TestWithPresentationLockSharesFirstmateIdentity(t *testing.T) {
	directory := t.TempDir()
	namespace := filepath.Join(directory, "firstmate-herdr-presentation")
	socket := filepath.Join(directory, "crew.sock")
	runner := &healthRunner{responses: []healthResponse{
		{out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + socket + `"}]}`},
		{out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + socket + `"}]}`},
	}}
	client := herdr.Client{SessionRunner: runner}
	key := sha256.Sum256([]byte("crew\x00" + socket))
	lockPath := filepath.Join(namespace, "order-"+hex.EncodeToString(key[:])[:32]+".lock")
	called := false
	err := withLock(context.Background(), client, "crew", namespace, func() error {
		called = true
		owner, err := os.Readlink(lockPath)
		if err != nil {
			return err
		}
		pid, err := os.ReadFile(filepath.Join(owner, "pid"))
		if err != nil || string(pid) != strconv.Itoa(os.Getpid())+"\n" {
			t.Fatalf("lock owner pid = %q, err = %v", pid, err)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("withPresentationLock called = %v, err = %v", called, err)
	}
	if len(runner.calls) != 2 || len(runner.calls[0].args) != 5 ||
		runner.calls[0].args[3] != "--session" || runner.calls[0].args[4] != "crew" {
		t.Fatalf("session lookup calls = %v", runner.calls)
	}
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock remains: %v", err)
	}
}

func TestWithPresentationLockRefusesChangedSocket(t *testing.T) {
	directory := t.TempDir()
	runner := &healthRunner{responses: []healthResponse{
		{
			out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + filepath.Join(
				directory,
				"one.sock",
			) + `"}]}`,
		},
		{
			out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + filepath.Join(
				directory,
				"two.sock",
			) + `"}]}`,
		},
	}}
	called := false
	err := withLock(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		"crew",
		filepath.Join(directory, "locks"),
		func() error {
			called = true
			return nil
		},
	)
	if err == nil || called {
		t.Fatalf("changed socket called = %v, err = %v", called, err)
	}
}

func TestWithPresentationLockRefusesAmbiguousSession(t *testing.T) {
	for _, body := range []string{
		`{"sessions":[]}`,
		`{"sessions":[{"name":"crew","running":false,"socket_path":"/tmp/crew.sock"}]}`,
		`{"sessions":[{"name":"crew","running":true,"socket_path":"/tmp/a"},{"name":"crew","running":true,"socket_path":"/tmp/b"}]}`,
		`{"sessions":[{"name":"crew","running":true,"socket_path":null}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			runner := &healthRunner{responses: []healthResponse{{out: body}}}
			called := false
			err := withLock(
				context.Background(),
				herdr.Client{SessionRunner: runner},
				"crew",
				filepath.Join(t.TempDir(), "locks"),
				func() error {
					called = true
					return nil
				},
			)
			if err == nil || called {
				t.Fatalf("ambiguous session called = %v, err = %v", called, err)
			}
		})
	}
}

func TestWithPresentationLockRefusesUnsafeNamespace(t *testing.T) {
	directory := t.TempDir()
	namespace := filepath.Join(directory, "locks")
	if err := os.Mkdir(namespace, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &healthRunner{
		responses: []healthResponse{
			{out: `{"sessions":[{"name":"crew","running":true,"socket_path":"/tmp/crew.sock"}]}`},
		},
	}
	err := withLock(
		context.Background(), herdr.Client{SessionRunner: runner},
		"crew",
		namespace,
		func() error { t.Fatal("unsafe callback"); return nil },
	)
	if err == nil {
		t.Fatal("unsafe namespace was accepted")
	}
}

func TestWithPresentationLockWaitsForExistingOwner(t *testing.T) {
	directory := t.TempDir()
	namespace := filepath.Join(directory, "locks")
	if err := os.Mkdir(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "crew.sock")
	key := sha256.Sum256([]byte("crew\x00" + socket))
	lockPath := filepath.Join(namespace, "order-"+hex.EncodeToString(key[:])[:32]+".lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &healthRunner{
		responses: []healthResponse{
			{out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + socket + `"}]}`},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	called := false
	err := withLock(
		ctx, herdr.Client{SessionRunner: runner},
		"crew",
		namespace,
		func() error {
			called = true
			return nil
		},
	)
	if err == nil || called {
		t.Fatalf("contended lock called = %v, err = %v", called, err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("existing owner removed: %v", err)
	}
}

func TestWithPresentationLockReleasesAfterOperationError(t *testing.T) {
	directory := t.TempDir()
	namespace := filepath.Join(directory, "locks")
	socket := filepath.Join(directory, "crew.sock")
	runner := &healthRunner{responses: []healthResponse{
		{out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + socket + `"}]}`},
		{out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + socket + `"}]}`},
	}}
	want := errors.New("operation failed")
	err := withLock(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		"crew",
		namespace,
		func() error { return want },
	)
	if !errors.Is(err, want) {
		t.Fatalf("operation error = %v", err)
	}
	entries, err := os.ReadDir(namespace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("lock entries = %v, err = %v", entries, err)
	}
}

func TestWithPresentationLockRecoversDeadOwner(t *testing.T) {
	directory := t.TempDir()
	namespace := filepath.Join(directory, "locks")
	if err := os.Mkdir(namespace, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "crew.sock")
	key := sha256.Sum256([]byte("crew\x00" + socket))
	lockPath := filepath.Join(namespace, "order-"+hex.EncodeToString(key[:])[:32]+".lock")
	owner := lockPath + ".owner.dead"
	if err := os.Mkdir(owner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "pid"), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(owner, lockPath); err != nil {
		t.Fatal(err)
	}
	runner := &healthRunner{responses: []healthResponse{
		{out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + socket + `"}]}`},
		{out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + socket + `"}]}`},
	}}
	called := false
	err := withLock(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		"crew",
		namespace,
		func() error { called = true; return nil },
	)
	if err != nil || !called {
		t.Fatalf("dead owner recovery called = %v, err = %v", called, err)
	}
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock remained after recovery: %v", err)
	}
}

func TestWithPresentationLockCanonicalizesSocketParent(t *testing.T) {
	directory := t.TempDir()
	physical := filepath.Join(directory, "physical")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	namespace := filepath.Join(directory, "locks")
	key := sha256.Sum256([]byte("crew\x00" + filepath.Join(physical, "crew.sock")))
	lockPath := filepath.Join(namespace, "order-"+hex.EncodeToString(key[:])[:32]+".lock")
	runner := &healthRunner{responses: []healthResponse{
		{
			out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + filepath.Join(
				alias,
				"crew.sock",
			) + `"}]}`,
		},
		{
			out: `{"sessions":[{"name":"crew","running":true,"socket_path":"` + filepath.Join(
				physical,
				"crew.sock",
			) + `"}]}`,
		},
	}}
	err := withLock(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		"crew",
		namespace,
		func() error {
			if _, err := os.Readlink(lockPath); err != nil {
				t.Fatalf("canonical lock path absent: %v", err)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

type healthResponse struct {
	out, stderr string
	err         error
}
type healthCall struct {
	session string
	args    []string
}
type healthRunner struct {
	responses []healthResponse
	calls     []healthCall
}

func (runner *healthRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, healthCall{session: session, args: args})
	if len(runner.responses) == 0 {
		panic("unexpected Herdr read")
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return []byte(response.out), []byte(response.stderr), response.err
}
