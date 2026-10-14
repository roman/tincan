package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestCommandRunnerSessionEnvironment(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/wrong/herdr.sock")
	t.Setenv("HERDR_WORKSPACE_ID", "w99")
	t.Setenv("HERDR_TAB_ID", "w99:t1")
	t.Setenv("HERDR_PANE_ID", "w99:p1")
	executable := filepath.Join(t.TempDir(), "herdr-probe")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\n' \"$HERDR_SESSION\" \"$HERDR_SOCKET_PATH\" \"$HERDR_WORKSPACE_ID\" \"$HERDR_TAB_ID\" \"$HERDR_PANE_ID\" \"$@\"\n"), 0700); err != nil {
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
	if err != nil || string(stdout) != "crew\n\n\n\n\npane\nget\npane-1\n--session\ncrew\n" {
		t.Fatalf("session command = %q, %v", stdout, err)
	}
}

func TestPromptPreservesTypedAdapterFailures(t *testing.T) {
	for _, check := range []struct {
		code string
		want error
	}{
		{"agent_not_found", ErrNotFound}, {"agent_not_ready", ErrAgentNotReady},
		{"workspace_not_found", ErrWorkspaceNotFound}, {"server_unavailable", ErrCommand},
	} {
		t.Run(check.code, func(t *testing.T) {
			runner := &fakeRunner{
				responses: []runResponse{
					{
						stderr: []byte(`{"error":{"code":"` + check.code + `"}}`),
						err:    errors.New("exit 1"),
					},
				},
			}
			err := (Client{Runner: runner}).Prompt(context.Background(), "worker", "Inbox pending")
			if !errors.Is(err, check.want) {
				t.Fatalf("prompt error=%v, want %v", err, check.want)
			}
		})
	}
}
