package firstmate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseLeaseActor(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "default", value: "", want: "main"},
		{name: "main", value: "main", want: "main"},
		{name: "branch", value: "branch", want: "branch"},
		{name: "unknown", value: "other", wantErr: true},
		{name: "space", value: " main", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor, err := ParseLeaseActor(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("ParseLeaseActor(%q) error = %v", test.value, err)
			}
			if !test.wantErr && actor.String() != test.want {
				t.Fatalf("ParseLeaseActor(%q) = %q", test.value, actor.String())
			}
		})
	}
}

func TestWithLeaseGuard(t *testing.T) {
	for _, test := range []struct {
		name     string
		lease    string
		session  string
		actor    string
		conflict bool
		remove   bool
	}{
		{name: "absent"},
		{name: "same actor", lease: "main\tPID\t1\n", session: "PID\n"},
		{name: "opposite actor", lease: "branch\tPID\t1\n", session: "PID\n", conflict: true},
		{name: "dead pid", lease: "branch\t99999999\t1\n", session: "PID\n", remove: true},
		{name: "malformed", lease: "branch\tbad\t1\n", session: "PID\n", remove: true},
		{name: "session mismatch", lease: "branch\tPID\t1\n", session: "99999999\n", remove: true},
		{name: "invalid session", lease: "branch\tPID\t1\n", session: "1\n", remove: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, id, _ := testLockInputs(t)
			pid := strconv.Itoa(os.Getpid())
			leasePath := filepath.Join(state.String(), ".lease-"+id.String())
			if test.lease != "" {
				lease := []byte(replacePID(test.lease, pid))
				if err := os.WriteFile(leasePath, lease, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if test.session != "" {
				if err := os.WriteFile(filepath.Join(state.String(), ".lock"), []byte(replacePID(test.session, pid)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			actor, err := ParseLeaseActor(test.actor)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			err = WithLeaseGuard(context.Background(), state, id, actor, func() error {
				called = true
				return nil
			})
			if test.conflict {
				if !errors.Is(err, ErrLeaseConflict) || called {
					t.Fatalf("conflict = %v, called = %v", err, called)
				}
			} else if err != nil || !called {
				t.Fatalf("guard = %v, called = %v", err, called)
			}
			data, readErr := os.ReadFile(leasePath)
			if test.remove || test.lease == "" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("stale lease remained: %q, %v", data, readErr)
				}
			} else if readErr != nil || string(data) != replacePID(test.lease, pid) {
				t.Fatalf("lease changed: %q, %v", data, readErr)
			}
		})
	}
}

func replacePID(value, pid string) string {
	return strings.ReplaceAll(value, "PID", pid)
}

func TestWithLeaseGuardRetainsCommandLock(t *testing.T) {
	state, id, _ := testLockInputs(t)
	mainActor, _ := ParseLeaseActor("main")
	entered := make(chan struct{})
	release := make(chan struct{})
	callbackErr := errors.New("callback failed")
	finished := make(chan error, 1)
	go func() {
		finished <- WithLeaseGuard(context.Background(), state, id, mainActor, func() error {
			close(entered)
			<-release
			return callbackErr
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	called := false
	err := WithLeaseGuard(ctx, state, id, mainActor, func() error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("wait = %v, called = %v", err, called)
	}
	close(release)
	if err := <-finished; !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v", err)
	}
	if err := WithLeaseGuard(context.Background(), state, id, mainActor, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWithLeaseGuardBashContention(t *testing.T) {
	source := os.Getenv("FIRSTMATE_SOURCE")
	if source == "" {
		t.Skip("set FIRSTMATE_SOURCE to run Bash interoperability test")
	}
	state, id, _ := testLockInputs(t)
	library := filepath.Join(source, "bin", "fm-lease-lib.sh")
	if _, err := os.Stat(library); err != nil {
		t.Fatal(err)
	}
	actor, _ := ParseLeaseActor("main")
	commandPath := filepath.Join(state.String(), ".fm-lease-command.lock")
	if err := WithLeaseGuard(context.Background(), state, id, actor, func() error {
		check := exec.Command("bash", "-c", ". \"$1\"; . \"$2\"; if fm_lock_try_acquire \"$3\"; then fm_lock_release \"$3\"; exit 1; fi", "bash", filepath.Join(source, "bin", "fm-wake-lib.sh"), library, commandPath)
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("Bash ignored Go lease guard: %v: %s", err, output)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ready := filepath.Join(state.String(), "bash-ready")
	release := filepath.Join(state.String(), "bash-release")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder := exec.CommandContext(
		ctx,
		"bash",
		"-c",
		". \"$1\"; fm_lease_guard task-1 probe; trap fm_lease_guard_release EXIT; : > \"$2\"; while [ ! -e \"$3\" ]; do sleep 0.01; done",
		"bash",
		library,
		ready,
		release,
	)
	holder.Env = append(os.Environ(), "STATE="+state.String(), "FM_SUPERVISION_ACTOR=branch")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		if holder.ProcessState == nil {
			_ = holder.Wait()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Bash did not acquire guard")
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitCtx, stopWait := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stopWait()
	called := false
	err := WithLeaseGuard(waitCtx, state, id, actor, func() error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("Go ignored Bash lease guard: %v, called = %v", err, called)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := WithLeaseGuard(context.Background(), state, id, actor, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
