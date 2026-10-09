package herdr

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBareComposerProof(t *testing.T) {
	const doorbell = ": Firstmate instruction waiting"
	for _, test := range []struct {
		name, kind, screen, want string
		proved                   bool
	}{
		{"claude empty", "claude", "❯ \n", "", true},
		{"codex old doorbell", "codex", "› " + doorbell + "\n", doorbell, true},
		{"muse dim ghost", "muse", "⟩ \x1b[2msuggestion\x1b[0m\n", "", true},
		{"devin empty", "devin", "❭ \n", "", true},
		{"foreign draft", "codex", "› other text\n", "other text", true},
		{"wrong glyph", "codex", "❯ \n", "", false},
		{"unsupported cursor", "cursor", "→ \n", "", false},
		{"footer", "claude", "❯ \nmodel: claude\n", "", false},
		{"overlay", "claude", "menu\n❯ \n", "", false},
		{"unknown escape", "claude", "\x1b[?25h❯ \n", "", false},
		{"plain ghost", "claude", "❯ suggestion\n", "suggestion", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			content, proved := bareComposer(test.kind, test.screen)
			if content != test.want || proved != test.proved {
				t.Fatalf("content=%q proved=%v", content, proved)
			}
		})
	}
}

type ringRunner struct {
	calls           []healthCall
	views           []string
	agentKind       string
	agentStatus     string
	agentStatuses   []string
	processStatus   string
	processStatuses []string
	failureAt       string
	processCalls    int
	onProcess       func(*ringRunner)
}

func (runner *ringRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, healthCall{session, args})
	command := strings.Join(args[:len(args)-2], " ")
	if command == runner.failureAt {
		return []byte(`{"error":{"code":"failed"}}`), nil, nil
	}
	switch {
	case strings.HasPrefix(command, "pane get "):
		return []byte(`{"result":{"pane":{"pane_id":"tab:1"}}}`), nil, nil
	case strings.HasPrefix(command, "agent get "):
		status := runner.agentStatus
		if len(runner.agentStatuses) > 0 {
			status = runner.agentStatuses[0]
			runner.agentStatuses = runner.agentStatuses[1:]
		}
		return []byte(
			`{"result":{"agent":{"pane_id":"tab:1","agent":"` + runner.agentKind + `","agent_status":"` + status + `"}}}`,
		), nil, nil
	case command == "status --json":
		return []byte(`{"server":{"running":true}}`), nil, nil
	case strings.HasPrefix(command, "pane process-info "):
		runner.processCalls++
		if runner.onProcess != nil {
			runner.onProcess(runner)
		}
		process := runner.processStatus
		if len(runner.processStatuses) > 0 {
			process = runner.processStatuses[0]
			runner.processStatuses = runner.processStatuses[1:]
		}
		return []byte(
			`{"result":{"type":"pane_process_info","process_info":{"pane_id":"tab:1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"` + process + `","argv0":"` + process + `"}]}}}`,
		), nil, nil
	case strings.HasPrefix(command, "pane read "):
		if len(runner.views) == 0 {
			panic("unexpected viewport read")
		}
		view := runner.views[0]
		runner.views = runner.views[1:]
		return []byte(view), nil, nil
	case strings.HasPrefix(command, "pane send-text "),
		strings.HasPrefix(command, "pane send-keys "):
		return []byte(`{"result":{}}`), nil, nil
	default:
		panic("unexpected Herdr command: " + command)
	}
}
func TestRingConservativeSubmission(t *testing.T) {
	const line = ": Firstmate instruction waiting"
	for _, test := range []struct {
		name                        string
		views                       []string
		kind, status, process, fail string
		statuses, processes         []string
		want                        RingOutcome
		textCalls, keyCalls         int
	}{
		{name: "empty composer", views: []string{"› \n"}, kind: "codex", status: "idle", process: "codex", want: RingSkipped},
		{name: "old doorbell enter only", views: []string{"› " + line + "\n", "› \n"}, kind: "codex", status: "idle", process: "codex", want: RingSent, keyCalls: 1},
		{name: "one enter retry", views: []string{"› " + line + "\n", "› " + line + "\n"}, kind: "codex", status: "idle", process: "codex", want: RingSent, keyCalls: 2},
		{name: "foreign draft", views: []string{"› human draft\n"}, kind: "codex", status: "idle", process: "codex", want: RingSkipped},
		{name: "footer", views: []string{"› \nfooter\n"}, kind: "codex", status: "idle", process: "codex", want: RingSkipped},
		{name: "unsupported cursor", kind: "cursor", status: "idle", process: "cursor-agent", want: RingSkipped},
		{name: "busy", kind: "codex", status: "working", process: "codex", want: RingSkipped},
		{name: "dead", kind: "codex", status: "idle", process: "zsh", want: RingUnavailable},
		{name: "dead before enter", views: []string{"› " + line + "\n"}, kind: "codex", status: "idle", process: "codex", processes: []string{"codex", "zsh"}, want: RingUnavailable},
		{name: "busy before enter", views: []string{"› " + line + "\n"}, kind: "codex", status: "idle", process: "codex", statuses: []string{"idle", "idle", "working"}, want: RingSkipped},
		{name: "busy after enter", views: []string{"› " + line + "\n"}, kind: "codex", status: "idle", process: "codex", statuses: []string{"idle", "idle", "idle", "working"}, want: RingSent, keyCalls: 1},
		{name: "unknown after enter", views: []string{"› " + line + "\n"}, kind: "codex", status: "idle", process: "codex", statuses: []string{"idle", "idle", "idle", "mystery"}, want: RingSent, keyCalls: 1},
		{name: "dead after enter", views: []string{"› " + line + "\n"}, kind: "codex", status: "idle", process: "codex", processes: []string{"codex", "codex", "zsh"}, want: RingSent, keyCalls: 1},

		{name: "structured read error", kind: "codex", status: "idle", process: "codex", fail: "pane read tab:1 --source visible --format ansi", want: RingFailed},
		{name: "structured enter error", views: []string{"› " + line + "\n"}, kind: "codex", status: "idle", process: "codex", fail: "pane send-keys tab:1 enter", want: RingFailed, keyCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, _ := ParseTarget("crew", "tab:1")
			runner := &ringRunner{
				views:           test.views,
				agentKind:       test.kind,
				agentStatus:     test.status,
				processStatus:   test.process,
				agentStatuses:   test.statuses,
				processStatuses: test.processes,
				failureAt:       test.fail,
			}
			client := Client{
				SessionRunner: runner,
				Processes:     fakeProcesses{rows: []Process{{PID: 100, PPID: 1, Name: "zsh"}}},
			}
			result := client.Ring(context.Background(), target, line)
			if result.Outcome != test.want {
				t.Fatalf("result=%+v", result)
			}
			if result.Outcome == RingFailed && !errors.Is(result.Err, ErrCommand) &&
				!errors.Is(result.Err, ErrResponse) {
				t.Fatalf("failure=%v", result.Err)
			}
			texts, keys := 0, 0
			for _, call := range runner.calls {
				if call.session != "crew" || len(call.args) < 2 ||
					!reflect.DeepEqual(
						call.args[len(call.args)-2:],
						[]string{"--session", "crew"},
					) {
					t.Fatalf("unscoped call=%+v", call)
				}
				switch call.args[1] {
				case "send-text":
					texts++
				case "send-keys":
					keys++
				}
			}
			if texts != test.textCalls || keys != test.keyCalls {
				t.Fatalf("text=%d keys=%d calls=%v", texts, keys, runner.calls)
			}
		})
	}
}

func TestRingRejectsControlTextBeforeHerdrRead(t *testing.T) {
	target, _ := ParseTarget("crew", "tab:1")
	result := (Client{}).Ring(context.Background(), target, "doorbell\x1b[31m")
	if result.Outcome != RingFailed || !errors.Is(result.Err, ErrResponse) {
		t.Fatalf("result=%+v", result)
	}
}

func TestRingReadsComposerAfterLastHealth(t *testing.T) {
	target, _ := ParseTarget("crew", "tab:1")
	runner := &ringRunner{
		views:         []string{"› " + ": Firstmate instruction waiting" + "\n"},
		agentKind:     "codex",
		agentStatus:   "idle",
		processStatus: "codex",
	}
	runner.onProcess = func(r *ringRunner) {
		if r.processCalls == 2 {
			r.views[0] = "› human draft\n"
		}
	}
	result := (Client{SessionRunner: runner}).Ring(
		context.Background(),
		target,
		": Firstmate instruction waiting",
	)
	if result.Outcome != RingSkipped {
		t.Fatalf("result=%+v", result)
	}
	for _, call := range runner.calls {
		if len(call.args) > 1 && (call.args[1] == "send-text" || call.args[1] == "send-keys") {
			t.Fatalf("mutation after changed composer: %v", runner.calls)
		}
	}
}

func TestRingRejectsWrappedAndSplitComposer(t *testing.T) {
	const line = ": Firstmate instruction waiting: list \"$FM_TASK_INBOX\"/*.msg in your 'task-1.inbox' steering inbox, read and act on each in numeric order, then mv each into its handled/."
	for _, test := range []struct {
		name, screen string
	}{
		{"wrapped full doorbell", "› " + line[:60] + "\n" + line[60:110] + "\n" + line[110:] + "\n"},
		{"partial composer plus footer", "› " + line[:60] + "\n" + line[60:] + "\n"},
		{"dim footer", "› " + line[:60] + "\n\x1b[2m" + line[60:] + "\x1b[0m\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, _ := ParseTarget("crew", "tab:1")
			runner := &ringRunner{
				views:         []string{test.screen},
				agentKind:     "codex",
				agentStatus:   "idle",
				processStatus: "codex",
			}
			result := (Client{SessionRunner: runner}).Ring(context.Background(), target, line)
			if result.Outcome != RingSkipped {
				t.Fatalf("result=%+v", result)
			}
			for _, call := range runner.calls {
				if len(call.args) > 1 &&
					(call.args[1] == "send-text" || call.args[1] == "send-keys") {
					t.Fatalf("mutation after unproven composer: %v", runner.calls)
				}
			}
		})
	}
}

func TestRingReadsAfterHealthBeforeEnter(t *testing.T) {
	const line = ": Firstmate instruction waiting"
	target, _ := ParseTarget("crew", "tab:1")
	runner := &ringRunner{
		views:         []string{"› " + line + "\n"},
		agentKind:     "codex",
		agentStatus:   "idle",
		processStatus: "codex",
	}
	runner.onProcess = func(r *ringRunner) {
		if r.processCalls == 2 {
			r.views[0] = "› human draft\n"
		}
	}
	result := (Client{SessionRunner: runner}).Ring(context.Background(), target, line)
	if result.Outcome != RingSkipped {
		t.Fatalf("result=%+v", result)
	}
	keys := 0
	for _, call := range runner.calls {
		if len(call.args) > 1 && call.args[1] == "send-keys" {
			keys++
		}
	}
	if keys != 0 {
		t.Fatalf("Enter sent after composer changed: %v", runner.calls)
	}
}

func TestRingReadsAfterHealthBeforeSecondEnter(t *testing.T) {
	const line = ": Firstmate instruction waiting"
	target, _ := ParseTarget("crew", "tab:1")
	runner := &ringRunner{
		views:         []string{"› " + line + "\n", "› " + line + "\n"},
		agentKind:     "codex",
		agentStatus:   "idle",
		processStatus: "codex",
	}
	runner.onProcess = func(r *ringRunner) {
		if r.processCalls == 3 {
			r.views[0] = "› human draft\n"
		}
	}
	result := (Client{SessionRunner: runner}).Ring(context.Background(), target, line)
	if result.Outcome != RingSent {
		t.Fatalf("result=%+v", result)
	}
	keys := 0
	for _, call := range runner.calls {
		if len(call.args) > 1 && call.args[1] == "send-keys" {
			keys++
		}
	}
	if keys != 1 {
		t.Fatalf("second Enter sent after composer changed: %v", runner.calls)
	}
}

func TestRingStripANSIPreservesNewlinesWhileDim(t *testing.T) {
	for _, test := range []struct {
		name, screen, wantRaw, wantTyped string
	}{
		{"unreset dim", "› \x1b[2mghost\nfooter", "› ghost\nfooter", "› \n"},
		{"reset after another row", "› \x1b[2mghost\nhint\x1b[0m\ntext", "› ghost\nhint\ntext", "› \n\ntext"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, typed, err := ringStripANSI(test.screen)
			if err != nil || raw != test.wantRaw || typed != test.wantTyped {
				t.Fatalf("raw=%q typed=%q err=%v", raw, typed, err)
			}
		})
	}
}

func TestRingSkipsFreshDoorbellWithoutAuthoritativeWidth(t *testing.T) {
	target, _ := ParseTarget("crew", "tab:1")
	runner := &ringRunner{
		views:         []string{"› \n"},
		agentKind:     "codex",
		agentStatus:   "idle",
		processStatus: "codex",
	}
	result := (Client{SessionRunner: runner}).Ring(
		context.Background(),
		target,
		": Firstmate instruction waiting",
	)
	if result.Outcome != RingSkipped {
		t.Fatalf("result=%+v", result)
	}
	for _, call := range runner.calls {
		if len(call.args) > 1 && (call.args[1] == "send-text" || call.args[1] == "send-keys") {
			t.Fatalf("terminal mutation without authoritative width: %v", runner.calls)
		}
	}
}
