package primary

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAuthorizeCallerIdentity(t *testing.T) {
	owner := Identity{Version: 2, Kind: "codex", SessionID: "primary", PID: 30,
		StartTicks: "30", BootID: "boot", PIDNamespace: "pid:[1]"}
	for _, check := range []struct {
		name           string
		ownerChange    func(*Identity)
		caller         Caller
		processes      map[int]Process
		wantAuthorized bool
	}{
		{name: "primary descendant", caller: Caller{PID: 10, UID: 1000}, wantAuthorized: true},
		{name: "primary itself", caller: Caller{PID: 30, UID: 1000}, wantAuthorized: true},
		{name: "another user", caller: Caller{PID: 10, UID: 1001}},
		{name: "invisible peer", caller: Caller{PID: 0, UID: 1000}},
		{name: "secondary Codex", caller: Caller{PID: 40, UID: 1000}, processes: map[int]Process{
			40: {PID: 40, ParentPID: 1, Kind: "codex", StartTicks: "40"},
		}},
		{name: "nested Codex worker", caller: Caller{PID: 40, UID: 1000}, processes: map[int]Process{
			40: {PID: 40, ParentPID: 30, Kind: "codex", StartTicks: "40"},
		}},
		{name: "nested Claude worker", caller: Caller{PID: 40, UID: 1000}, processes: map[int]Process{
			40: {PID: 40, ParentPID: 30, Kind: "claude", StartTicks: "40"},
		}},
		{name: "exited owner", caller: Caller{PID: 10, UID: 1000}, processes: map[int]Process{30: {}}},
		{name: "reused owner PID", caller: Caller{PID: 10, UID: 1000}, processes: map[int]Process{
			30: {PID: 30, ParentPID: 1, Kind: "codex", StartTicks: "new"},
		}},
		{name: "changed owner harness", caller: Caller{PID: 10, UID: 1000}, processes: map[int]Process{
			30: {PID: 30, ParentPID: 1, Kind: "claude", StartTicks: "30"},
		}},
		{name: "another boot", caller: Caller{PID: 10, UID: 1000}, ownerChange: func(i *Identity) { i.BootID = "other" }},
		{name: "foreign namespace", caller: Caller{PID: 10, UID: 1000}, ownerChange: func(i *Identity) { i.PIDNamespace = "pid:[2]" }},
		{name: "invalid owner", caller: Caller{PID: 10, UID: 1000}, ownerChange: func(i *Identity) { i.SessionID = "" }},
		{name: "unreadable peer", caller: Caller{PID: 99, UID: 1000}},
		{name: "ancestry cycle", caller: Caller{PID: 40, UID: 1000}, processes: map[int]Process{
			40: {PID: 40, ParentPID: 50, StartTicks: "40"},
			50: {PID: 50, ParentPID: 40, StartTicks: "50"},
		}},
		{name: "no harness ancestor", caller: Caller{PID: 40, UID: 1000}, processes: map[int]Process{
			40: {PID: 40, ParentPID: 1, StartTicks: "40"},
			1:  {PID: 1, ParentPID: 0, StartTicks: "1"},
		}},
	} {
		t.Run(check.name, func(t *testing.T) {
			current := owner
			if check.ownerChange != nil {
				check.ownerChange(&current)
			}
			processes := map[int]Process{
				10: {PID: 10, ParentPID: 20, StartTicks: "10"},
				20: {PID: 20, ParentPID: 30, StartTicks: "20"},
				30: {PID: 30, ParentPID: 1, Kind: "codex", StartTicks: "30"},
			}
			for pid, process := range check.processes {
				processes[pid] = process
			}
			lookup := func(pid int) (Process, error) {
				process, ok := processes[pid]
				if !ok || process.PID == 0 {
					return Process{}, os.ErrNotExist
				}
				return process, nil
			}
			err := authorizeCallerIdentity(current, check.caller, 1000, "boot", "pid:[1]", lookup)
			if check.wantAuthorized {
				if err != nil {
					t.Fatalf("authorize caller: %v", err)
				}
			} else if !errors.Is(err, ErrUnauthorizedCaller) {
				t.Fatalf("authorize caller = %v, want unauthorized", err)
			}
		})
	}
}

func TestAuthorizeCallerProcess(t *testing.T) {
	if os.Getenv("TINCAN_CALLER_PROCESS_TEST") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		path := filepath.Join(t.TempDir(), "codex")
		destination, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(destination, source)
		if err := errors.Join(copyErr, destination.Close()); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(path, "-test.run=^TestAuthorizeCallerProcess$")
		command.Env = append(os.Environ(), "TINCAN_CALLER_PROCESS_TEST=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native process fixture: %v\n%s", err, output)
		}
		return
	}
	process, err := readProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if process.Kind != "codex" {
		t.Fatalf("fixture kind = %q", process.Kind)
	}
	boot, err := readBootID()
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := readPIDNamespace()
	if err != nil {
		t.Fatal(err)
	}
	owner := Identity{Version: 2, Kind: process.Kind, SessionID: "fixture",
		PID: process.PID, StartTicks: process.StartTicks, BootID: boot, PIDNamespace: namespace}
	caller := Caller{PID: process.PID, UID: uint32(os.Geteuid())}
	for _, scenario := range []string{"valid", "missing", "malformed", "replaced", "locked"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			current := owner
			if scenario == "replaced" {
				current.StartTicks = "other-process"
			}
			if scenario != "missing" {
				if err := writeOwner(root, current); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "malformed" {
				if err := os.WriteFile(filepath.Join(root, "primary.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "locked" {
				lock, err := os.OpenFile(
					filepath.Join(root, "primary.claim.lock"),
					os.O_CREATE|os.O_RDWR,
					0600,
				)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
					t.Fatal(err)
				}
				result := make(chan error, 1)
				go func() { result <- AuthorizeCaller(root, caller) }()
				select {
				case err := <-result:
					t.Fatalf("authorization ignored held claim lock: %v", err)
				case <-time.After(50 * time.Millisecond):
				}
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("authorization did not resume after claim unlock")
				}
				return
			}
			err := AuthorizeCaller(root, caller)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrUnauthorizedCaller) {
				t.Fatalf("authorization = %v, want unauthorized", err)
			}
		})
	}
}
