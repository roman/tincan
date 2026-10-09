package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

func (runner *healthRunner) Run(context.Context, ...string) ([]byte, []byte, error) {
	panic("unscoped Herdr read")
}

func (runner *healthRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, healthCall{session, args})
	if len(runner.responses) == 0 {
		panic("unexpected Herdr read")
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return []byte(response.out), []byte(response.stderr), response.err
}

type fakeProcesses struct {
	rows []Process
	err  error
}

func (processes fakeProcesses) Snapshot(context.Context) ([]Process, error) {
	return processes.rows, processes.err
}

func TestParseTarget(t *testing.T) {
	for _, test := range []struct {
		session, pane string
		valid         bool
	}{
		{"crew", "pane-1", true}, {"", "pane-1", false}, {"crew", "", false},
		{"crew other", "pane-1", false}, {"crew", "pane/1", false},
	} {
		_, err := ParseTarget(test.session, test.pane)
		if (err == nil) != test.valid {
			t.Errorf("ParseTarget(%q,%q) = %v", test.session, test.pane, err)
		}
	}
}

func TestConfirmPaneGoneRequiresExactNotFound(t *testing.T) {
	for _, test := range []struct {
		name      string
		response  healthResponse
		wantError error
	}{
		{"missing pane", healthResponse{stderr: `{"error":{"code":"pane_not_found"}}`, err: errors.New("exit 1")}, nil},
		{"missing pane on stdout", healthResponse{out: `{"error":{"code":"pane_not_found"}}`}, nil},
		{"present pane", healthResponse{out: `{"result":{"pane":{"pane_id":"pane-1"}}}`}, ErrPanePresent},
		{"missing agent", healthResponse{stderr: `{"error":{"code":"agent_not_found"}}`, err: errors.New("exit 1")}, ErrCommand},
		{"server unavailable", healthResponse{stderr: `{"error":{"code":"server_unavailable"}}`, err: errors.New("exit 1")}, ErrCommand},
		{"conflicting errors", healthResponse{out: `{"error":{"code":"pane_not_found"}}`, stderr: `{"error":{"code":"server_unavailable"}}`, err: errors.New("exit 1")}, ErrCommand},
		{"same response conflict", healthResponse{out: `{"result":{},"error":{"code":"pane_not_found"}}`}, ErrCommand},
		{"conflicting result", healthResponse{out: `{"error":{"code":"pane_not_found"}}`, stderr: `{"result":{"pane":{"pane_id":"pane-1"}}}`}, ErrCommand},
		{"unstructured failure", healthResponse{err: errors.New("transport failed")}, ErrCommand},
		{"wrong pane", healthResponse{out: `{"result":{"pane":{"pane_id":"pane-2"}}}`}, ErrResponse},
		{"empty response", healthResponse{}, ErrResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, err := ParseTarget("crew", "pane-1")
			if err != nil {
				t.Fatal(err)
			}
			runner := &healthRunner{responses: []healthResponse{test.response}}
			err = (Client{SessionRunner: runner}).ConfirmPaneGone(context.Background(), target)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("ConfirmPaneGone error = %v, want %v", err, test.wantError)
			}
			if !reflect.DeepEqual(
				runner.calls,
				[]healthCall{{"crew", []string{"pane", "get", "pane-1", "--session", "crew"}}},
			) {
				t.Fatalf("calls = %v", runner.calls)
			}
		})
	}
	if err := (Client{}).ConfirmPaneGone(context.Background(), Target{}); !errors.Is(
		err,
		ErrResponse,
	) {
		t.Fatalf("invalid target error = %v", err)
	}
}
func TestHealth(t *testing.T) {
	const pane = `{"result":{"pane":{"pane_id":"pane-1"}}}`
	const working = `{"result":{"agent":{"pane_id":"pane-1","agent_status":"working"}}}`
	const idle = `{"result":{"agent":{"pane_id":"pane-1","agent_status":"idle"}}}`
	const processHarness = `{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}`
	const processShell = `{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":100,"name":"zsh","argv0":"zsh"}]}}}`
	for _, test := range []struct {
		name      string
		responses []healthResponse
		processes fakeProcesses
		want      EndpointHealth
		activity  ActivityHealth
		calls     [][]string
	}{
		{"pane missing zero exit", []healthResponse{{out: `{"error":{"code":"pane_not_found"}}`}}, fakeProcesses{}, Missing, HealthUnknown, [][]string{{"pane", "get", "pane-1"}}},
		{"pane missing nonzero", []healthResponse{{stderr: `{"error":{"code":"pane_not_found"}}`, err: errors.New("exit 1")}}, fakeProcesses{}, Missing, HealthUnknown, [][]string{{"pane", "get", "pane-1"}}},
		{"structured error outranks partial stdout", []healthResponse{{out: pane, stderr: `{"error":{"code":"pane_not_found"}}`, err: errors.New("exit 1")}}, fakeProcesses{}, Missing, HealthUnknown, [][]string{{"pane", "get", "pane-1"}}},
		{"agent missing zero exit", []healthResponse{{out: pane}, {out: `{"error":{"code":"agent_not_found"}}`}}, fakeProcesses{}, Dead, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}}},
		{"agent missing nonzero", []healthResponse{{out: pane}, {stderr: `{"error":{"code":"agent_not_found"}}`, err: errors.New("exit 1")}}, fakeProcesses{}, Dead, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}}},
		{"foreground missing pid", []healthResponse{{out: pane}, {out: working}, {out: `{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"name":"codex"}]}}}`}, {out: `{"server":{"running":true}}`}}, fakeProcesses{}, Unreadable, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}, {"status", "--json"}}},
		{"working harness", []healthResponse{{out: pane}, {out: working}, {out: processHarness}}, fakeProcesses{}, Alive, HealthBusy, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}}},
		{"done harness", []healthResponse{{out: pane}, {out: `{"result":{"agent":{"pane_id":"pane-1","agent_status":"done"}}}`}, {out: processHarness}}, fakeProcesses{}, Alive, HealthIdle, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}}},
		{"blocked harness", []healthResponse{{out: pane}, {out: `{"result":{"agent":{"pane_id":"pane-1","agent_status":"blocked"}}}`}, {out: processHarness}}, fakeProcesses{}, Alive, HealthIdle, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}}},

		{"idle shell only", []healthResponse{{out: pane}, {out: idle}, {out: processShell}}, fakeProcesses{rows: []Process{{PID: 100, PPID: 1, Name: "zsh"}}}, Dead, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}}},
		{"working shell descendant", []healthResponse{{out: pane}, {out: working}, {out: processShell}}, fakeProcesses{rows: []Process{{PID: 100, PPID: 1, Name: "zsh"}, {PID: 102, PPID: 100, Name: "codex", Args: "codex"}}}, Alive, HealthBusy, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}}},
		{"harness in descendant argv0", []healthResponse{{out: pane}, {out: working}, {out: processShell}}, fakeProcesses{rows: []Process{{PID: 100, PPID: 1, Name: "zsh"}, {PID: 102, PPID: 100, Name: "bash", Args: "codex --quiet"}}}, Alive, HealthBusy, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}}},
		{"nested shell only", []healthResponse{{out: pane}, {out: idle}, {out: processShell}}, fakeProcesses{rows: []Process{{PID: 100, PPID: 1, Name: "zsh"}, {PID: 102, PPID: 100, Name: "bash"}}}, Dead, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}}},
		{"unknown descendant", []healthResponse{{out: pane}, {out: idle}, {out: processShell}}, fakeProcesses{rows: []Process{{PID: 100, PPID: 1, Name: "zsh"}, {PID: 102, PPID: 100, Name: "node"}}}, Dead, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}}},

		{"pane mismatch", []healthResponse{{out: `{"result":{"pane":{"pane_id":"other"}}}`}, {out: `{"server":{"running":true}}`}}, fakeProcesses{}, Unreadable, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"status", "--json"}}},
		{"stopped server", []healthResponse{{out: `garbage`}, {out: `{"server":{"running":false}}`}}, fakeProcesses{}, Missing, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"status", "--json"}}},
		{"transport unknown", []healthResponse{{err: errors.New("network")}, {out: `{"server":{"running":true}}`}}, fakeProcesses{}, Unreadable, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"status", "--json"}}},
		{"process unreadable", []healthResponse{{out: pane}, {out: working}, {out: `{"result":{}}`}, {out: `{"server":{"running":true}}`}}, fakeProcesses{}, Unreadable, HealthUnknown, [][]string{{"pane", "get", "pane-1"}, {"agent", "get", "pane-1"}, {"pane", "process-info", "--pane", "pane-1"}, {"status", "--json"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, _ := ParseTarget("crew", "pane-1")
			runner := &healthRunner{responses: test.responses}
			got := (Client{SessionRunner: runner, Processes: test.processes}).Health(
				context.Background(),
				target,
			)
			if got.Endpoint != test.want || got.Activity != test.activity {
				t.Fatalf("Health = %+v", got)
			}
			var want []healthCall
			for _, args := range test.calls {
				want = append(want, healthCall{"crew", append(args, "--session", "crew")})
			}
			if !reflect.DeepEqual(runner.calls, want) {
				t.Fatalf("calls = %v, want %v", runner.calls, want)
			}
		})
	}
}

func TestClassifyProcess(t *testing.T) {
	for _, test := range []struct {
		name, argv0, args string
		want              processClass
	}{
		{"bash", "node", "", processOther},
		{"zsh", "-zsh", "", processShell},
		{"bash", "bash", "", processShell},
		{"sh", "sh", "", processShell},
		{"dash", "dash", "", processShell},
		{"ash", "ash", "", processShell},
		{"ksh", "ksh", "", processShell},
		{"mksh", "mksh", "", processShell},
		{"tcsh", "tcsh", "", processShell},
		{"csh", "csh", "", processShell},
		{"fish", "fish", "", processShell},
		{"node", "pi", "", processAgent},
		{"codex-linux-x64", "", "", processAgent},
		{"2.1.220", "/home/roman/.local/share/claude/versions/2.1.220", "", processAgent},
		{"muse-bin-1", "", "", processAgent},
		{"node", "/home/roman/.local/share/cursor-agent/versions/1/agent", "", processAgent},
		{"musescore", "", "", processOther},
		{"ompd", "", "", processOther},
		{"node", "node", "node /opt/@google/gemini-cli/dist/index.js", processAgent},
		{"node", "node", "node --inspect /opt/unrelated.js --prompt gemini", processOther},
	} {
		if got := classifyProcess(test.name, test.argv0, test.args); got != test.want {
			t.Errorf(
				"classifyProcess(%q,%q,%q) = %v, want %v",
				test.name,
				test.argv0,
				test.args,
				got,
				test.want,
			)
		}
	}
}

func TestHealthSettlesOtherForeground(t *testing.T) {
	target, _ := ParseTarget("crew", "pane-1")
	runner := &healthRunner{responses: []healthResponse{
		{out: `{"result":{"pane":{"pane_id":"pane-1"}}}`},
		{out: `{"result":{"agent":{"pane_id":"pane-1","agent_status":"idle"}}}`},
		{
			out: `{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"starship","argv0":"starship"}]}}}`,
		},
		{
			out: `{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":100,"name":"zsh","argv0":"zsh"}]}}}`,
		},
	}}
	got := (Client{SessionRunner: runner, Processes: fakeProcesses{rows: []Process{{PID: 100, PPID: 1, Name: "zsh"}}}}).Health(
		context.Background(),
		target,
	)
	if got.Endpoint != Dead {
		t.Fatalf("Health = %+v", got)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("calls = %v", runner.calls)
	}
}

func TestHealthStopsAfterTenOtherSamples(t *testing.T) {
	target, _ := ParseTarget("crew", "pane-1")
	responses := []healthResponse{
		{out: `{"result":{"pane":{"pane_id":"pane-1"}}}`},
		{out: `{"result":{"agent":{"pane_id":"pane-1","agent_status":"working"}}}`},
	}
	for range 10 {
		responses = append(
			responses,
			healthResponse{
				out: `{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"starship","argv0":"starship"}]}}}`,
			},
		)
	}
	runner := &healthRunner{responses: responses}
	got := (Client{SessionRunner: runner}).Health(context.Background(), target)
	if got.Endpoint != Alive || got.Activity != HealthBusy {
		t.Fatalf("Health = %+v", got)
	}
	if len(runner.calls) != 12 {
		t.Fatalf("calls = %d, want 12", len(runner.calls))
	}
}

func TestCommandRunnerSessionEnvironment(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "herdr-probe")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\n' \"$HERDR_SESSION\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := (commandRunner{executable: executable}).RunSession(
		context.Background(),
		"crew",
		"pane",
		"get",
		"pane-1",
		"--session",
		"crew",
	)
	if err != nil || string(stdout) != "crew\npane\nget\npane-1\n--session\ncrew\n" {
		t.Fatalf("session command = %q, %v", stdout, err)
	}
}
