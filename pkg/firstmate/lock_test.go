package firstmate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLockInputs(t *testing.T) (AbsolutePath, TaskID, string) {
	t.Helper()
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := ParseTaskID("task-1")
	if err != nil {
		t.Fatal(err)
	}
	return state, id, filepath.Join(state.String(), ".meta-task-1.lock")
}

func TestTaskLockLiveContentionAndRelease(t *testing.T) {
	state, id, path := testLockInputs(t)
	first, err := AcquireTaskLock(state, id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(path)
	if err != nil || filepath.Dir(target) != state.String() {
		t.Fatalf("owner target = %q, %v", target, err)
	}
	pid, err := os.ReadFile(filepath.Join(target, "pid"))
	if err != nil || string(pid) != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("owner pid = %q, %v", pid, err)
	}
	if _, err := AcquireTaskLock(state, id, 50*time.Millisecond); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("contended acquire = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released link remains: %v", err)
	}
	second, err := AcquireTaskLock(state, id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskLockAbandonedLockFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		legacy bool
	}{
		{name: "symlink owner"},
		{name: "legacy directory", legacy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, id, path := testLockInputs(t)
			owner := path
			if !test.legacy {
				owner = path + ".owner.stale"
			}
			if err := os.Mkdir(owner, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(owner, "pid"), []byte("99999999\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if !test.legacy {
				if err := os.Symlink(owner, path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := AcquireTaskLock(state, id, 50*time.Millisecond); !errors.Is(
				err,
				ErrLockTimeout,
			) {
				t.Fatalf("abandoned lock acquire = %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("abandoned lock was changed: %v", err)
			}
		})
	}
}

func TestTaskLockStealMarkerBlocksClaim(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "regular file",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "live owner symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				owner := path + ".owner.live"
				if err := os.Mkdir(owner, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(owner, "pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(owner, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "dangling symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Symlink(path+".owner.missing", path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, id, path := testLockInputs(t)
			test.setup(t, path+".steal")
			if _, err := AcquireTaskLock(state, id, 50*time.Millisecond); !errors.Is(
				err,
				ErrLockTimeout,
			) {
				t.Fatalf("steal marker acquire = %v", err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("claim survived steal marker: %v", err)
			}
		})
	}
}

func TestTaskLockOwnerSymlinkCannotDeleteVictim(t *testing.T) {
	state, id, _ := testLockInputs(t)
	lock, err := AcquireTaskLock(state, id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(state.String(), "victim")
	if err := os.Mkdir(victim, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"pid":  strconv.Itoa(os.Getpid()) + "\n",
		"role": "keep this file",
	} {
		if err := os.WriteFile(filepath.Join(victim, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(lock.ownerPath, "pid")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lock.ownerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, lock.ownerPath); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); !errors.Is(err, ErrNotLockOwner) {
		t.Fatalf("release with swapped owner = %v", err)
	}
	for _, name := range []string{"pid", "role"} {
		if _, err := os.Stat(filepath.Join(victim, name)); err != nil {
			t.Fatalf("victim %s changed: %v", name, err)
		}
	}
}

func TestTaskLockSerializesGoroutines(t *testing.T) {
	state, id, _ := testLockInputs(t)
	var active, maximum atomic.Int32
	const workers = 12
	var group sync.WaitGroup
	errorsSeen := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			lock, err := AcquireTaskLock(state, id, 3*time.Second)
			if err != nil {
				errorsSeen <- err
				return
			}
			now := active.Add(1)
			for {
				previous := maximum.Load()
				if now <= previous || maximum.CompareAndSwap(previous, now) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			if err := lock.Release(); err != nil {
				errorsSeen <- err
			}
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent holders = %d", maximum.Load())
	}
}

func TestTaskLockConcurrentRelease(t *testing.T) {
	state, id, _ := testLockInputs(t)
	for range 100 {
		lock, err := AcquireTaskLock(state, id, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		copyOfLock := *lock
		start := make(chan struct{})
		results := make(chan error, 2)
		for _, holder := range []*TaskLock{lock, &copyOfLock} {
			go func() {
				<-start
				results <- holder.Release()
			}()
		}
		close(start)
		first, second := <-results, <-results
		oneSuccess := (first == nil && errors.Is(second, ErrNotLockOwner)) ||
			(second == nil && errors.Is(first, ErrNotLockOwner))
		if !oneSuccess {
			t.Fatalf("concurrent releases = %v, %v", first, second)
		}
	}
}

func TestTaskLockBashInteroperability(t *testing.T) {
	source := os.Getenv("FIRSTMATE_SOURCE")
	if source == "" {
		t.Skip("set FIRSTMATE_SOURCE to run Bash interoperability test")
	}
	state, id, path := testLockInputs(t)
	library := filepath.Join(source, "bin", "fm-wake-lib.sh")
	if _, err := os.Stat(library); err != nil {
		t.Fatal(err)
	}
	first, err := AcquireTaskLock(state, id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	check := exec.Command("bash", "-c",
		". \"$1\"; if fm_lock_try_acquire \"$2\"; then fm_lock_release \"$2\"; exit 1; fi",
		"bash", library, path)
	check.Env = append(os.Environ(), "FM_STATE_OVERRIDE="+state.String())
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("Bash ignored Go lock: %v: %s", err, output)
	}
	if err := first.Release(); err != nil {
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
		". \"$1\"; fm_lock_try_acquire \"$2\" || exit 2; : > \"$3\"; while [ ! -e \"$4\" ]; do sleep 0.01; done; fm_lock_release \"$2\"",
		"bash",
		library,
		path,
		ready,
		release,
	)
	holder.Env = append(os.Environ(), "FM_STATE_OVERRIDE="+state.String())
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
			t.Fatal("Bash did not acquire lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := AcquireTaskLock(state, id, 50*time.Millisecond); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("Go ignored Bash lock: %v", err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatal(err)
	}
	last, err := AcquireTaskLock(state, id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := last.Release(); err != nil {
		t.Fatal(err)
	}
}
