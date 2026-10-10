package primary

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Hook struct {
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
}

type Process struct {
	PID        int
	ParentPID  int
	Kind       string
	StartTicks string
}

type Identity struct {
	Version      int    `json:"version"`
	Kind         string `json:"kind"`
	SessionID    string `json:"session_id"`
	PID          int    `json:"pid"`
	StartTicks   string `json:"start_ticks"`
	BootID       string `json:"boot_id"`
	PIDNamespace string `json:"pid_namespace"`
}

func (identity Identity) validate() error {
	if err := identity.validateCore(); err != nil {
		return err
	}
	if identity.Version != 2 || identity.PIDNamespace == "" {
		return errors.New("identity needs version 2 and a PID namespace")
	}
	return nil
}

func (identity Identity) validateCore() error {
	if identity.Kind != "claude" && identity.Kind != "codex" {
		return errors.New("identity must name Claude or Codex")
	}
	if identity.SessionID == "" || strings.ContainsAny(identity.SessionID, "\r\n") {
		return errors.New("identity needs a single-line session ID")
	}
	if identity.PID < 1 || identity.StartTicks == "" || identity.BootID == "" {
		return errors.New("identity needs a live process and boot ID")
	}
	return nil
}

func classifyExecutable(executable string) string {
	executable = strings.TrimSuffix(executable, " (deleted)")
	base := filepath.Base(executable)
	switch base {
	case "claude", ".claude-wrapped":
		return "claude"
	case "codex", ".codex-wrapped":
		return "codex"
	}
	parts := strings.Split(filepath.Clean(executable), string(filepath.Separator))
	if len(parts) >= 3 &&
		(parts[len(parts)-3] == "claude" || parts[len(parts)-3] == ".claude") &&
		parts[len(parts)-2] == "versions" {
		return "claude"
	}
	return ""
}

func readProcess(pid int) (Process, error) {
	path := fmt.Sprintf("/proc/%d", pid)
	data, err := os.ReadFile(filepath.Join(path, "stat"))
	if err != nil {
		return Process{}, err
	}
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 {
		return Process{}, errors.New("process stat lacks a command boundary")
	}
	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) <= 19 {
		return Process{}, errors.New("process stat lacks a start time")
	}
	parentPID, err := strconv.Atoi(fields[1])
	if err != nil {
		return Process{}, err
	}
	executable, err := os.Readlink(filepath.Join(path, "exe"))
	if err != nil {
		return Process{}, err
	}
	return Process{
		PID:        pid,
		ParentPID:  parentPID,
		Kind:       classifyExecutable(executable),
		StartTicks: fields[19],
	}, nil
}

func readBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readPIDNamespace() (string, error) {
	return os.Readlink("/proc/self/ns/pid")
}

func ResolveHookIdentity(
	hook Hook,
	environment map[string]string,
	parentPID int,
	lookup func(int) (Process, error),
	bootID string,
	pidNamespace string,
) (Identity, error) {
	if hook.SessionID == "" || strings.ContainsAny(hook.SessionID, "\r\n") {
		return Identity{}, errors.New("hook needs a valid session ID")
	}
	pid := parentPID
	for range 16 {
		if pid < 1 {
			break
		}
		process, err := lookup(pid)
		if err != nil {
			return Identity{}, err
		}
		switch process.Kind {
		case "claude":
			if environment["CLAUDE_CODE_SESSION_ID"] != hook.SessionID {
				return Identity{}, errors.New("Claude hook session does not match its process")
			}
			modelPID, err := strconv.Atoi(environment["CLAUDE_PID"])
			if err != nil {
				return Identity{}, errors.New("Claude model process ID is unavailable")
			}
			for process.Kind == "claude" {
				if process.PID == modelPID {
					return Identity{
						Version:      2,
						Kind:         "claude",
						SessionID:    hook.SessionID,
						PID:          process.PID,
						StartTicks:   process.StartTicks,
						BootID:       bootID,
						PIDNamespace: pidNamespace,
					}, nil
				}
				process, err = lookup(process.ParentPID)
				if err != nil {
					return Identity{}, err
				}
			}
			return Identity{}, errors.New("Claude model process is outside the hook ancestry")
		case "codex":
			if environment["CODEX_SESSION_ID"] != hook.SessionID {
				return Identity{}, errors.New("Codex hook session does not match its process")
			}
			return Identity{Version: 2, Kind: "codex", SessionID: hook.SessionID, PID: process.PID,
				StartTicks: process.StartTicks, BootID: bootID, PIDNamespace: pidNamespace}, nil
		}
		pid = process.ParentPID
	}
	return Identity{}, errors.New("no Claude or Codex process found in hook ancestry")
}

func CurrentHookIdentity(hook Hook) (Identity, error) {
	bootID, err := readBootID()
	if err != nil {
		return Identity{}, err
	}
	pidNamespace, err := readPIDNamespace()
	if err != nil {
		return Identity{}, err
	}
	environment := map[string]string{
		"CLAUDE_CODE_SESSION_ID": os.Getenv("CLAUDE_CODE_SESSION_ID"),
		"CLAUDE_PID":             os.Getenv("CLAUDE_PID"),
		"CODEX_SESSION_ID":       os.Getenv("CODEX_SESSION_ID"),
	}
	return ResolveHookIdentity(hook, environment, os.Getppid(), readProcess, bootID, pidNamespace)
}
