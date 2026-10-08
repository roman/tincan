package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testMetadata = "window=crew:pane-1\nendpoint_task_id=task-1\nworktree=/repo/worktree\nproject=/repo\nharness=codex\nkind=ship\nspawn_gen=s123.456.7\nbackend=herdr\nherdr_session=crew\nherdr_workspace_id=workspace-1\nherdr_tab_id=tab:1\nherdr_pane_id=pane-1\n"

func cliFixture(t *testing.T) (string, string, string) {
	t.Helper()
	state := t.TempDir()
	log := filepath.Join(t.TempDir(), "herdr.log")
	if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte(testMetadata), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "herdr")
	script := "#!/bin/sh\nprintf 'session=%s\\nargs=%s\\n' \"$HERDR_SESSION\" \"$*\" >> \"$HERDR_LOG\"\nif [ \"$HERDR_FAIL_SEND\" = 1 ] && [ \"$2\" = send-text ]; then exit 9; fi\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_LOG", log)
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return state, executable, log
}
func callCLI(args []string, input string) (int, string, string) {
	var out, errs bytes.Buffer
	code := run(args, strings.NewReader(input), &out, &errs)
	return code, out.String(), errs.String()
}
func TestFirstmateSendBodySourcesAndDeferredDoorbell(t *testing.T) {
	for _, test := range []struct {
		name        string
		source      func(t *testing.T) []string
		input, body string
	}{
		{name: "inline multiline", source: func(*testing.T) []string { return []string{"--body", "line one\nline two"} }, body: "line one\nline two"},
		{name: "stdin", source: func(*testing.T) []string { return []string{"--body-file", "-"} }, input: "from stdin\nsecond", body: "from stdin\nsecond"},
		{name: "file", source: func(t *testing.T) []string {
			path := filepath.Join(t.TempDir(), "body")
			if err := os.WriteFile(path, []byte("from file\n"), 0600); err != nil {
				t.Fatal(err)
			}
			return []string{"--body-file", path}
		}, body: "from file\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, _, log := cliFixture(t)
			args := []string{"--state", state, "task-1"}
			args = append(args, test.source(t)...)
			code, out, errText := callCLI(args, test.input)
			if code != 0 || errText != "" || !strings.Contains(out, "status: recorded\nrecord: ") ||
				!strings.Contains(out, "doorbell: deferred\n") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
			}
			records, err := filepath.Glob(filepath.Join(state, "task-1.inbox", "*.msg"))
			if err != nil || len(records) != 1 {
				t.Fatalf("records=%v err=%v", records, err)
			}
			body, err := os.ReadFile(records[0])
			if err != nil || !strings.Contains(string(body), "\n--\n"+test.body) ||
				!strings.HasPrefix(string(body), "schema=fm-task-inbox.v1\nat=") {
				t.Fatalf("record=%q err=%v", body, err)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatalf("Herdr was invoked: %v", err)
			}
		})
	}
}
func TestFirstmateSendExitContracts(t *testing.T) {
	for _, test := range []struct {
		name     string
		args     []string
		input    string
		setup    func(t *testing.T, state string)
		want     int
		record   bool
		doorbell string
	}{
		{name: "missing source", want: 2}, {name: "removed Herdr option", args: []string{"--herdr", "unused", "--body", "message"}, want: 2}, {name: "both sources", args: []string{"--body", "a", "--body-file", "-"}, want: 2},
		{name: "blank", args: []string{"--body", "  \n"}, want: 1},
		{name: "invalid metadata", args: []string{"--body", "message"}, setup: func(t *testing.T, state string) {
			if err := os.WriteFile(filepath.Join(state, "task-1.meta"), []byte("bad"), 0600); err != nil {
				t.Fatal(err)
			}
		}, want: 1},
		{name: "opposite lease", args: []string{"--body", "message"}, setup: func(t *testing.T, state string) {
			pid := strconv.Itoa(os.Getpid())
			if err := os.WriteFile(filepath.Join(state, ".lock"), []byte(pid+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, ".lease-task-1"), []byte("branch\t"+pid+"\t1\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}, want: 6},
		{name: "record only", args: []string{"--body", "message"}, want: 0, record: true, doorbell: "deferred"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, _, _ := cliFixture(t)
			if test.setup != nil {
				test.setup(t, state)
			}
			args := []string{"--state", state, "task-1"}
			args = append(args, test.args...)
			code, out, _ := callCLI(args, test.input)
			if code != test.want {
				t.Fatalf("exit=%d output=%q", code, out)
			}
			records, _ := filepath.Glob(filepath.Join(state, "task-1.inbox", "*.msg"))
			if (len(records) == 1) != test.record {
				t.Fatalf("records=%v", records)
			}
			if test.doorbell != "" && !strings.Contains(out, "doorbell: "+test.doorbell+"\n") {
				t.Fatalf("output=%q", out)
			}
		})
	}
}

func TestFirstmateSendEnvironmentDefaultsAndOverrides(t *testing.T) {
	state, _, _ := cliFixture(t)
	t.Setenv("FM_SUPERVISION_ACTOR", "branch")
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(state, ".lock"), []byte(pid+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, ".lease-task-1"), []byte("branch\t"+pid+"\t1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, _ := callCLI(
		[]string{"--state", state, "--actor", "main", "task-1", "--body", "first"},
		"",
	)
	if code != 6 {
		t.Fatalf("explicit actor exit=%d", code)
	}
	code, out, errText := callCLI([]string{"--state", state, "task-1", "--body", "second"}, "")
	if code != 0 || errText != "" || !strings.Contains(out, "doorbell: deferred\n") {
		t.Fatalf("environment defaults exit=%d out=%q err=%q", code, out, errText)
	}
}

func TestFirstmateSendMalformedSelectorsAreUsageErrors(t *testing.T) {
	for _, test := range []struct {
		name        string
		state, task string
	}{
		{name: "relative state", state: "relative", task: "task-1"},
		{name: "invalid task", task: "task/1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, _, _ := cliFixture(t)
			if test.state != "" {
				state = test.state
			}
			code, _, _ := callCLI([]string{"--state", state, test.task, "--body", "message"}, "")
			if code != 2 {
				t.Fatalf("exit=%d, want usage exit 2", code)
			}
		})
	}
}
