package herdr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type fakeRunner struct {
	responses []runResponse
	calls     [][]string
}

type runResponse struct {
	stdout []byte
	stderr []byte
	err    error
}

func (r *fakeRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	r.calls = append(r.calls, args)
	if len(r.responses) == 0 {
		panic("unexpected command")
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.stdout, response.stderr, response.err
}

func TestGet(t *testing.T) {
	tests := []struct {
		name      string
		response  runResponse
		want      Agent
		wantError error
	}{
		{
			name: "working agent",
			response: runResponse{
				stdout: []byte(
					`{"result":{"agent":{"agent":"codex","agent_status":"working","pane_id":"w1:p2"}}}`,
				),
			},
			want: Agent{PaneID: "w1:p2", Status: Working, Kind: "codex"},
		},
		{
			name: "unknown is present",
			response: runResponse{
				stdout: []byte(`{"result":{"agent":{"agent_status":"unknown","pane_id":"w1:p2"}}}`),
			},
			want: Agent{PaneID: "w1:p2", Status: Unknown},
		},
		{
			name: "missing agent on stdout",
			response: runResponse{
				stdout: []byte(`{"error":{"code":"agent_not_found","message":"not found"}}`),
				err:    errors.New("exit status 1"),
			},
			wantError: ErrNotFound,
		},
		{
			name: "missing agent",
			response: runResponse{
				stderr: []byte(`{"error":{"code":"agent_not_found","message":"not found"}}`),
				err:    errors.New("exit status 1"),
			},
			wantError: ErrNotFound,
		},
		{
			name: "server error",
			response: runResponse{
				stderr: []byte(`{"error":{"code":"server_unavailable","message":"offline"}}`),
				err:    errors.New("exit status 1"),
			},
			wantError: ErrCommand,
		},
		{
			name:      "malformed success",
			response:  runResponse{stdout: []byte(`{"result":{}}`)},
			wantError: ErrResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{responses: []runResponse{tt.response}}
			got, err := (Client{Runner: runner}).Get(context.Background(), "worker")
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Get error = %v, want %v", err, tt.wantError)
			}
			if err == nil && got != tt.want {
				t.Fatalf("Get = %+v, want %+v", got, tt.want)
			}
			if !reflect.DeepEqual(runner.calls, [][]string{{"agent", "get", "worker"}}) {
				t.Fatalf("calls = %v", runner.calls)
			}
		})
	}
}

func TestStartRequiresMissingAgent(t *testing.T) {
	missing := runResponse{
		stderr: []byte(`{"error":{"code":"agent_not_found"}}`),
		err:    errors.New("exit status 1"),
	}
	valid := runResponse{
		stdout: []byte(
			`{"result":{"agent":{"agent":"codex","agent_status":"idle","pane_id":"w1:p2"}}}`,
		),
	}
	tests := []struct {
		name      string
		get       runResponse
		verify    runResponse
		wantError error
		wantCalls int
	}{
		{name: "missing then start", get: missing, verify: valid, wantCalls: 3},
		{
			name:      "name lookup missing",
			get:       missing,
			verify:    missing,
			wantCalls: 3,
			wantError: ErrNotFound,
		},
		{
			name: "different pane after start",
			get:  missing,
			verify: runResponse{
				stdout: []byte(
					`{"result":{"agent":{"agent":"codex","agent_status":"idle","pane_id":"w1:p3"}}}`,
				),
			},
			wantCalls: 3,
			wantError: ErrResponse,
		},
		{
			name: "different kind after start",
			get:  missing,
			verify: runResponse{
				stdout: []byte(
					`{"result":{"agent":{"agent":"claude","agent_status":"idle","pane_id":"w1:p2"}}}`,
				),
			},
			wantCalls: 3,
			wantError: ErrResponse,
		},
		{
			name: "unknown after start",
			get:  missing,
			verify: runResponse{
				stdout: []byte(
					`{"result":{"agent":{"agent":"codex","agent_status":"unknown","pane_id":"w1:p2"}}}`,
				),
			},
			wantCalls: 3,
			wantError: ErrResponse,
		},
		{
			name: "missing kind after start",
			get:  missing,
			verify: runResponse{
				stdout: []byte(`{"result":{"agent":{"agent_status":"idle","pane_id":"w1:p2"}}}`),
			},
			wantCalls: 3,
			wantError: ErrResponse,
		},
		{
			name: "unknown agent",
			get: runResponse{
				stdout: []byte(`{"result":{"agent":{"agent_status":"unknown","pane_id":"w1:p2"}}}`),
			},
			wantCalls: 1,
			wantError: ErrAgentPresent,
		},
		{
			name: "transient error",
			get: runResponse{
				stderr: []byte(`{"error":{"code":"server_unavailable"}}`),
				err:    errors.New("exit status 1"),
			},
			wantCalls: 1,
			wantError: ErrCommand,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			responses := []runResponse{tt.get}
			if tt.wantCalls == 3 {
				responses = append(
					responses,
					runResponse{stdout: []byte(`{"result":{}}`)},
					tt.verify,
				)
			}
			runner := &fakeRunner{responses: responses}
			err := (Client{Runner: runner}).Start(context.Background(), "w1:p2", "worker", "codex")
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Start error = %v, want %v", err, tt.wantError)
			}
			if len(runner.calls) != tt.wantCalls {
				t.Fatalf("calls = %v, want %d", runner.calls, tt.wantCalls)
			}
			if tt.wantCalls == 3 {
				if fmt.Sprint(runner.calls[1]) != "[agent start worker --kind codex --pane w1:p2]" {
					t.Fatalf("start call = %v", runner.calls[1])
				}
				if !reflect.DeepEqual(runner.calls[2], []string{"agent", "get", "worker"}) {
					t.Fatalf("verification lookup = %v", runner.calls[2])
				}
			}
		})
	}
}

func TestStartUnknownInteractiveReadiness(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		ready     bool
		wantError error
	}{
		{name: "Codex ready", kind: "codex", ready: true},
		{name: "Codex not ready", kind: "codex", ready: false, wantError: ErrResponse},
		{name: "other agent ready", kind: "claude", ready: true, wantError: ErrResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{responses: []runResponse{
				{
					stderr: []byte(`{"error":{"code":"agent_not_found"}}`),
					err:    errors.New("exit status 1"),
				},
				{stdout: []byte(`{"result":{}}`)},
				{stdout: []byte(fmt.Sprintf(
					`{"result":{"agent":{"agent":%q,"agent_status":"unknown","pane_id":"w1:p2","interactive_ready":%t}}}`,
					tt.kind,
					tt.ready,
				))},
			}}
			err := (Client{Runner: runner}).Start(context.Background(), "w1:p2", "worker", tt.kind)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Start error = %v, want %v", err, tt.wantError)
			}
		})
	}
}

func TestWaitSettled(t *testing.T) {
	runner := &fakeRunner{responses: []runResponse{
		{stdout: []byte(`{"result":{}}`)},
		{stdout: []byte(`{"result":{"agent":{"agent_status":"done","pane_id":"w1:p2"}}}`)},
	}}
	agent, err := (Client{Runner: runner}).WaitSettled(context.Background(), "worker")
	if err != nil || agent.Status != Done {
		t.Fatalf("WaitSettled = %+v, %v", agent, err)
	}
	if !reflect.DeepEqual(
		runner.calls[0],
		[]string{"agent", "wait", "worker", "--timeout", "10000"},
	) {
		t.Fatalf("wait call = %v", runner.calls[0])
	}
}

func TestPrompt(t *testing.T) {
	runner := &fakeRunner{responses: []runResponse{{stdout: []byte(`{"result":{}}`)}}}
	err := (Client{Runner: runner}).Prompt(context.Background(), "worker", "Inbox has messages.")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agent", "prompt", "worker", "Inbox has messages."}
	if !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("call = %v, want %v", runner.calls[0], want)
	}
}

func TestStartWithArgs(t *testing.T) {
	runner := &fakeRunner{responses: []runResponse{
		{stderr: []byte(`{"error":{"code":"agent_not_found"}}`), err: errors.New("exit status 1")},
		{stdout: []byte(`{"result":{}}`)},
		{
			stdout: []byte(
				`{"result":{"agent":{"agent":"codex","agent_status":"idle","pane_id":"w1:p2"}}}`,
			),
		},
	}}
	err := (Client{Runner: runner}).StartWithArgs(
		context.Background(),
		"w1:p2",
		"worker",
		"codex",
		[]string{"--model", "gpt-6", "resume"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls[2][2] != "worker" {
		t.Fatalf("verification lookup = %v, want worker name", runner.calls[2])
	}
	want := []string{
		"agent",
		"start",
		"worker",
		"--kind",
		"codex",
		"--pane",
		"w1:p2",
		"--",
		"--model",
		"gpt-6",
		"resume",
	}
	if !reflect.DeepEqual(runner.calls[1], want) {
		t.Fatalf("start call = %v, want %v", runner.calls[1], want)
	}
}
