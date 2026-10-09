package herdr

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type sessionCall struct {
	session string
	args    []string
}

type scriptedSessionRunner struct {
	responses []runResponse
	calls     []sessionCall
}

func (runner *scriptedSessionRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, sessionCall{session: session, args: args})
	if len(runner.responses) == 0 {
		panic("unexpected session command")
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return response.stdout, response.stderr, response.err
}

func TestForSessionBindsEveryStartCommand(t *testing.T) {
	runner := &scriptedSessionRunner{responses: []runResponse{
		{stderr: []byte(`{"error":{"code":"agent_not_found"}}`), err: errors.New("exit 1")},
		{stdout: []byte(`{"result":{}}`)},
		{
			stdout: []byte(
				`{"result":{"agent":{"agent":"codex","agent_status":"idle","pane_id":"pane-1"}}}`,
			),
		},
	}}
	client, err := (Client{SessionRunner: runner}).ForSession("work")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.StartWithArgs(context.Background(), "pane-1", "worker-1", "codex", []string{"resume", "thread-1"}); err != nil {
		t.Fatal(err)
	}
	want := []sessionCall{
		{session: "work", args: []string{"agent", "get", "pane-1"}},
		{
			session: "work",
			args: []string{
				"agent",
				"start",
				"worker-1",
				"--kind",
				"codex",
				"--pane",
				"pane-1",
				"--",
				"resume",
				"thread-1",
			},
		},
		{session: "work", args: []string{"agent", "get", "worker-1"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %+v, want %+v", runner.calls, want)
	}
}

func TestForSessionRejectsUnusableSession(t *testing.T) {
	for _, test := range []struct {
		name    string
		session string
		runner  SessionRunner
	}{
		{name: "invalid session", session: "bad:session", runner: &scriptedSessionRunner{}},
		{name: "missing runner", session: "work"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (Client{SessionRunner: test.runner}).ForSession(test.session); err == nil {
				t.Fatal("expected session binding error")
			}
		})
	}
}

func TestForegroundDirectoryReadsLivePanePath(t *testing.T) {
	for _, test := range []struct {
		name      string
		response  string
		want      string
		wantError bool
	}{
		{name: "live path", response: `{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":"/worktree"}}}`, want: "/worktree"},
		{name: "creation path only", response: `{"result":{"pane":{"pane_id":"pane-1","cwd":"/worktree"}}}`, wantError: true},
		{name: "relative path", response: `{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":"worktree"}}}`, wantError: true},
		{name: "unclean path", response: `{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":"/worktree/../other"}}}`, wantError: true},
		{name: "wrong pane", response: `{"result":{"pane":{"pane_id":"pane-2","foreground_cwd":"/worktree"}}}`, wantError: true},
		{name: "missing pane", response: `{"result":{"pane":{"foreground_cwd":"/worktree"}}}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedSessionRunner{
				responses: []runResponse{{stdout: []byte(test.response)}},
			}
			client, err := (Client{SessionRunner: runner}).ForSession("work")
			if err != nil {
				t.Fatal(err)
			}
			path, err := client.ForegroundDirectory(context.Background(), "pane-1")
			if (err != nil) != test.wantError || path != test.want {
				t.Fatalf("path %q, error %v", path, err)
			}
			if !reflect.DeepEqual(
				runner.calls,
				[]sessionCall{{session: "work", args: []string{"pane", "get", "pane-1"}}},
			) {
				t.Fatalf("calls = %+v", runner.calls)
			}
		})
	}
}
