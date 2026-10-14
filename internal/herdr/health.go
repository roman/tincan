package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrPanePresent = errors.New("Herdr pane is still present")

// Target binds every Herdr health read to one named session and pane.
type Target struct{ session, paneID string }

// ParseTarget validates the exact session and pane to inspect.
func ParseTarget(session, paneID string) (Target, error) {
	if !healthAtom(session, false) || !healthAtom(paneID, true) {
		return Target{}, ErrResponse
	}
	return Target{session: session, paneID: paneID}, nil
}
func healthAtom(value string, colon bool) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if strings.ContainsRune(
			"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._@%+-",
			r,
		) {
			continue
		}
		if colon && r == ':' {
			continue
		}
		return false
	}
	return true
}

// Session returns the target's validated Herdr session.
func (target Target) Session() string { return target.session }

// PaneID returns the target's validated pane ID.
func (target Target) PaneID() string { return target.paneID }

type EndpointHealth string

const (
	Alive      EndpointHealth = "alive"
	Dead       EndpointHealth = "dead"
	Missing    EndpointHealth = "missing"
	Unreadable EndpointHealth = "unreadable"
)

type ActivityHealth string

const (
	HealthBusy    ActivityHealth = "busy"
	HealthIdle    ActivityHealth = "idle"
	HealthUnknown ActivityHealth = "unknown"
)

// HealthStatus separates endpoint presence from the agent's activity.
type HealthStatus struct {
	Endpoint EndpointHealth
	Activity ActivityHealth
}

// SessionRunner adds explicit session targeting to Herdr reads.
type SessionRunner interface {
	RunSession(context.Context, string, ...string) ([]byte, []byte, error)
}

// Process describes one process in a snapshot of the local process tree.
type Process struct {
	PID, PPID  int
	Name, Args string
}

// ProcessInspector reads the local process tree for shell-only proof.
type ProcessInspector interface {
	Snapshot(context.Context) ([]Process, error)
}

type psInspector struct{}

func (psInspector) Snapshot(ctx context.Context) ([]Process, error) {
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,comm=,args=").Output()
	if err != nil {
		return nil, err
	}
	var rows []Process
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		rows = append(
			rows,
			Process{PID: pid, PPID: ppid, Name: fields[2], Args: strings.Join(fields[3:], " ")},
		)
	}
	return rows, nil
}

func (client Client) sessionRead(
	ctx context.Context,
	target Target,
	args ...string,
) ([]byte, []byte, error) {
	if client.SessionRunner == nil {
		return nil, nil, ErrCommand
	}
	return client.SessionRunner.RunSession(
		ctx,
		target.session,
		append(args, "--session", target.session)...)
}

func healthBody(stdout, stderr []byte) (json.RawMessage, string) {
	var result json.RawMessage
	for _, data := range [][]byte{stdout, stderr} {
		var body struct {
			Result json.RawMessage `json:"result"`
			Error  struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &body) != nil {
			continue
		}
		if body.Error.Code != "" {
			return nil, body.Error.Code
		}
		if len(result) == 0 && len(body.Result) > 0 {
			result = body.Result
		}
	}
	return result, ""
}

// ConfirmPaneGone requires a structured absence for the exact recorded pane.
// A stopped server or a missing agent leaves the pane's fate uncertain.
func (client Client) ConfirmPaneGone(ctx context.Context, target Target) error {
	if target.session == "" || target.paneID == "" {
		return ErrResponse
	}
	stdout, stderr, err := client.sessionRead(ctx, target, "pane", "get", target.paneID)
	missing := false
	present := false
	for _, data := range [][]byte{stdout, stderr} {
		if len(data) == 0 {
			continue
		}
		var response struct {
			Result json.RawMessage `json:"result"`
			Error  struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &response) != nil {
			return ErrResponse
		}
		if response.Error.Code != "" {
			if len(response.Result) > 0 || response.Error.Code != "pane_not_found" {
				return fmt.Errorf("%w: conflicting pane lookup response", ErrCommand)
			}
			missing = true
			continue
		}
		var result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		}
		if json.Unmarshal(response.Result, &result) != nil || result.Pane.PaneID != target.paneID {
			return ErrResponse
		}
		present = true
	}
	if missing && present {
		return fmt.Errorf("%w: conflicting pane lookup response", ErrCommand)
	}
	if missing {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: pane lookup: %v", ErrCommand, err)
	}
	if present {
		return ErrPanePresent
	}
	return ErrResponse
}

// Health classifies one exact Herdr endpoint with session-scoped reads.
// Ambiguous responses stay unreadable unless the named server is stopped.
func (client Client) Health(ctx context.Context, target Target) HealthStatus {
	unknown := HealthStatus{Endpoint: Unreadable, Activity: HealthUnknown}
	if target.session == "" || target.paneID == "" {
		return unknown
	}
	stdout, stderr, _ := client.sessionRead(ctx, target, "pane", "get", target.paneID)
	result, code := healthBody(stdout, stderr)
	if code == "pane_not_found" {
		return HealthStatus{Endpoint: Missing, Activity: HealthUnknown}
	}
	var pane struct {
		Pane struct {
			PaneID string `json:"pane_id"`
		} `json:"pane"`
	}
	if code != "" || json.Unmarshal(result, &pane) != nil || pane.Pane.PaneID != target.paneID {
		return client.unreadableOrStopped(ctx, target)
	}
	stdout, stderr, _ = client.sessionRead(ctx, target, "agent", "get", target.paneID)
	result, code = healthBody(stdout, stderr)
	if code == "agent_not_found" {
		return HealthStatus{Endpoint: Dead, Activity: HealthUnknown}
	}
	var agent struct {
		Agent struct {
			PaneID string     `json:"pane_id"`
			Status AgentState `json:"agent_status"`
		} `json:"agent"`
	}
	if code != "" || json.Unmarshal(result, &agent) != nil ||
		(agent.Agent.PaneID != "" && agent.Agent.PaneID != target.paneID) {
		return client.unreadableOrStopped(ctx, target)
	}
	activity := classifyActivity(agent.Agent.Status)
	if activity == HealthUnknown {
		return client.unreadableOrStopped(ctx, target)
	}
	for attempt := 0; attempt < 10; attempt++ {
		verdict, shellPID := client.processSample(ctx, target)
		switch verdict {
		case processAgent:
			return HealthStatus{Endpoint: Alive, Activity: activity}
		case processShell:
			if client.Processes == nil {
				return client.unreadableOrStopped(ctx, target)
			}
			rows, err := client.Processes.Snapshot(ctx)
			if err != nil {
				return client.unreadableOrStopped(ctx, target)
			}
			switch descendantHealth(shellPID, rows) {
			case Alive:
				return HealthStatus{Endpoint: Alive, Activity: activity}
			case Dead:
				return HealthStatus{Endpoint: Dead, Activity: HealthUnknown}
			default:
				return client.unreadableOrStopped(ctx, target)
			}
		case processUnreadable:
			return client.unreadableOrStopped(ctx, target)
		}
		if attempt == 9 {
			return HealthStatus{Endpoint: Alive, Activity: activity}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return client.unreadableOrStopped(ctx, target)
		case <-timer.C:
		}
	}
	return client.unreadableOrStopped(ctx, target)
}

func (client Client) processSample(ctx context.Context, target Target) (processClass, int) {
	stdout, stderr, _ := client.sessionRead(
		ctx,
		target,
		"pane",
		"process-info",
		"--pane",
		target.paneID,
	)
	result, code := healthBody(stdout, stderr)
	var process struct {
		Type        string `json:"type"`
		ProcessInfo struct {
			PaneID     string `json:"pane_id"`
			ShellPID   int    `json:"shell_pid"`
			Foreground []struct {
				PID     int      `json:"pid"`
				Name    string   `json:"name"`
				Argv0   string   `json:"argv0"`
				Cmdline string   `json:"cmdline"`
				Argv    []string `json:"argv"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if code != "" || json.Unmarshal(result, &process) != nil ||
		process.Type != "pane_process_info" ||
		process.ProcessInfo.PaneID != target.paneID ||
		process.ProcessInfo.ShellPID <= 1 ||
		process.ProcessInfo.Foreground == nil {
		return processUnreadable, 0
	}
	other := false
	for _, foreground := range process.ProcessInfo.Foreground {
		if foreground.PID <= 1 ||
			(foreground.Name == "" && foreground.Argv0 == "" && len(foreground.Argv) == 0 && foreground.Cmdline == "") {
			return processUnreadable, 0
		}
		argv0 := foreground.Argv0
		if len(foreground.Argv) > 0 {
			argv0 = foreground.Argv[0]
		}
		args := foreground.Cmdline
		if args == "" {
			args = strings.Join(foreground.Argv, " ")
		}
		switch classifyProcess(foreground.Name, argv0, args) {
		case processAgent:
			return processAgent, process.ProcessInfo.ShellPID
		case processOther:
			other = true
		}
	}
	if other {
		return processOther, process.ProcessInfo.ShellPID
	}
	return processShell, process.ProcessInfo.ShellPID
}

func (client Client) unreadableOrStopped(ctx context.Context, target Target) HealthStatus {
	stdout, stderr, _ := client.sessionRead(ctx, target, "status", "--json")
	_, code := healthBody(stdout, stderr)
	if code == "" {
		for _, data := range [][]byte{stdout, stderr} {
			var status struct {
				Server struct {
					Running *bool `json:"running"`
				} `json:"server"`
			}
			if json.Unmarshal(data, &status) == nil && status.Server.Running != nil &&
				!*status.Server.Running {
				return HealthStatus{Endpoint: Missing, Activity: HealthUnknown}
			}
		}
	}
	return HealthStatus{Endpoint: Unreadable, Activity: HealthUnknown}
}

func classifyActivity(status AgentState) ActivityHealth {
	switch status {
	case Working:
		return HealthBusy
	case Idle, Done, Blocked:
		return HealthIdle
	default:
		return HealthUnknown
	}
}

type processClass uint8

const (
	processOther processClass = iota
	processAgent
	processShell
	processUnreadable
)

func classifyProcess(name, argv0, args string) processClass {
	byName := classifyProcessName(name, argv0)
	if byName == processAgent {
		return processAgent
	}
	byArgv0 := byName
	if argv0 != "" {
		byArgv0 = classifyProcessName(argv0, argv0)
	}
	if byArgv0 == processAgent || geminiArgs(args) {
		return processAgent
	}
	if byName == processShell && byArgv0 == processShell {
		return processShell
	}
	return processOther
}

func classifyProcessName(path, argv0 string) processClass {
	base := strings.TrimPrefix(filepath.Base(path), "-")
	switch base {
	case "muse", "pi", "pi-signed", "pi-launcher", "Pi", "omp", "agy", "devin":
		return processAgent
	case "zsh", "bash", "sh", "dash", "ash", "ksh", "mksh", "tcsh", "csh", "fish":
		return processShell
	}
	if strings.HasPrefix(base, "muse-bin-") {
		return processAgent
	}
	for _, fragment := range []string{"claude", "codex", "opencode", "grok", "kimi", "rovo"} {
		if strings.Contains(base, fragment) {
			return processAgent
		}
	}
	for _, candidate := range []string{path, argv0} {
		for _, component := range []string{"claude", "codex", "opencode", "grok", "kimi", "pi-signed", "pi", "omp"} {
			for _, part := range strings.Split(candidate, "/") {
				if part == component {
					return processAgent
				}
			}
		}
	}
	if cursorProcess(path, argv0) {
		return processAgent
	}
	return processOther
}

func cursorProcess(path, argv0 string) bool {
	base := strings.TrimPrefix(filepath.Base(path), "-")
	if base == "cursor-agent" {
		return true
	}
	cursorPath := func(value string) bool {
		return filepath.Base(value) == "cursor-agent" ||
			strings.Contains(value, "/cursor-agent/versions/")
	}
	if !strings.Contains(path, "/") && base != "agent" && base != "MainThread" &&
		!strings.HasPrefix(base, "node") &&
		!strings.HasPrefix(base, "python") {
		return false
	}
	return cursorPath(path) || cursorPath(argv0)
}

func geminiArgs(args string) bool {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return false
	}
	geminiPath := func(value string) bool {
		return !strings.HasPrefix(value, "-") &&
			(filepath.Base(value) == "gemini" || strings.Contains(value, "/@google/gemini-cli/"))
	}
	if geminiPath(fields[0]) {
		return true
	}
	base := filepath.Base(fields[0])
	if base != "MainThread" && base != "node" && !strings.HasPrefix(base, "node-") &&
		!(strings.HasPrefix(base, "node") && len(base) > 4 && base[4] >= '0' && base[4] <= '9') {
		return false
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") {
			continue
		}
		return geminiPath(field)
	}
	return false
}
func descendantHealth(shellPID int, rows []Process) EndpointHealth {
	known := map[int]bool{shellPID: true}
	shellFound := false
	for _, row := range rows {
		if row.PID == shellPID {
			shellFound = true
			break
		}
	}
	if !shellFound {
		return Unreadable
	}
	for changed := true; changed; {
		changed = false
		for _, row := range rows {
			if known[row.PPID] && !known[row.PID] {
				known[row.PID] = true
				changed = true
			}
		}
	}
	for _, row := range rows {
		if row.PID == shellPID || !known[row.PID] {
			continue
		}
		args := strings.Fields(row.Args)
		argv0 := ""
		if len(args) > 0 {
			argv0 = args[0]
		}
		if classifyProcess(row.Name, argv0, row.Args) == processAgent {
			return Alive
		}
	}
	return Dead
}
