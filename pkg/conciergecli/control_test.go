package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func controlFixture(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	executable := filepath.Join(directory, "herdr-control")
	record := filepath.Join(directory, "calls")
	script := `#!/bin/sh
printf '%s|%s|%s|%s\n' "$HERDR_SESSION" "$HERDR_SOCKET_PATH" "$HERDR_PANE_ID" "$*" >> "$TEST_CONTROL_CALLS"
case "$1 $2" in
 'agent get')
  if [ "$TEST_CONTROL_START" = 1 ] && [ ! -f "$TEST_CONTROL_STARTED" ]; then printf '%s\n' '{"error":{"code":"agent_not_found"}}'; exit 1; fi
  if [ "$TEST_CONTROL_FAIL_VERIFY" = 1 ] && [ -f "$TEST_CONTROL_STARTED" ] && [ "$3" = pane-1 ]; then exit 72; fi
  status=idle
  if [ "$TEST_CONTROL_BUSY" = 1 ] && [ ! -f "$TEST_CONTROL_SETTLED" ]; then status=working; fi
  printf '{"result":{"agent":{"pane_id":"pane-1","agent_status":"%s","agent":"codex"}}}\n' "$status" ;;
 'agent send-keys') printf settled > "$TEST_CONTROL_SETTLED"; printf '%s\n' '{"result":{}}' ;;
 'agent wait') printf '%s\n' '{"result":{}}' ;;
 'agent start') printf started > "$TEST_CONTROL_STARTED"; printf '%s\n' '{"result":{}}' ;;
 *) exit 73 ;;
esac
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINCAN_HERDR", executable)
	t.Setenv("TEST_CONTROL_CALLS", record)
	t.Setenv("TEST_CONTROL_STARTED", filepath.Join(directory, "started"))
	t.Setenv("TEST_CONTROL_SETTLED", filepath.Join(directory, "settled"))
	t.Setenv("TEST_CONTROL_START", "")
	t.Setenv("TEST_CONTROL_BUSY", "")
	t.Setenv("TEST_CONTROL_FAIL_VERIFY", "")
	t.Setenv("HERDR_SESSION", "ambient")
	t.Setenv("HERDR_SOCKET_PATH", "ambient-socket")
	t.Setenv("HERDR_PANE_ID", "ambient-pane")
	return executable, record
}

func runControlCLI(t *testing.T, args ...string) string {
	t.Helper()
	var output bytes.Buffer
	args = append(
		[]string{
			"--root",
			"relative-unused",
			"--message-root",
			"relative-unused",
			"--recipient",
			"../unused",
		},
		args...)
	if code := Run(context.Background(), args, strings.NewReader(""), &output, io.Discard); code != 0 {
		t.Fatalf("control: %d %s", code, &output)
	}
	return output.String()
}

func TestControlSessionAndExecutableSelection(t *testing.T) {
	for _, session := range []string{"ambient", "explicit"} {
		t.Run(session, func(t *testing.T) {
			executable, record := controlFixture(t)
			args := []string{"control"}
			prefix := "ambient|ambient-socket|ambient-pane|"
			if session == "explicit" {
				args = append(args, "--session", "work")
				prefix = "work|||"
			}
			if got := runControlCLI(t, append(args, "status", "worker")...); got != "pane_id: \"pane-1\"\nstatus: idle\n" {
				t.Fatalf("status: %s", got)
			}
			calls, err := os.ReadFile(record)
			if err != nil || string(calls) != prefix+"agent get worker\n" {
				t.Fatalf("routing: %s %v", calls, err)
			}
			t.Setenv("TINCAN_HERDR", "/missing/executable")
			runControlCLI(t, append(args, "--herdr", executable, "status", "worker")...)
		})
	}
}

func TestControlInterruptAndStartForwarding(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	for _, action := range []string{"interrupt", "start"} {
		t.Run(action, func(t *testing.T) {
			executable, record := controlFixture(t)
			root := shortTaskRoot(t)
			serveControlFixture(t, root, executable)
			args := []string{"control", "--session", "work", action}
			var commands []string
			if action == "interrupt" {
				t.Setenv("TEST_CONTROL_BUSY", "1")
				args = append(args, "worker")
				commands = []string{
					"agent get worker",
					"agent get pane-1",
					"agent send-keys pane-1 esc",
					"agent wait pane-1 --timeout 10000",
					"agent get pane-1",
				}
			} else {
				t.Setenv("TEST_CONTROL_START", "1")
				args = append(args, "--pane", "pane-1", "--name", "worker", "--kind", "codex")
				commands = []string{"agent get pane-1", "agent start worker --kind codex --pane pane-1", "agent get worker", "agent get pane-1"}
			}
			if got := invoke(t, root, "", args...); got != "pane_id: \"pane-1\"\nstatus: idle\n" {
				t.Fatalf("action: %s", got)
			}
			calls, err := os.ReadFile(record)
			want := ""
			for _, command := range commands {
				want += "work|||" + command + "\n"
			}
			if err != nil || string(calls) != want {
				t.Fatalf("forwarding: %s %v", calls, err)
			}
		})
	}
}

func TestControlRejectsUsageBeforeHerdrAndReportsVerificationFailure(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	executable, record := controlFixture(t)
	for _, args := range [][]string{{"control"}, {"control", "status", "worker", "--unknown"}, {"control", "--help"}} {
		var output bytes.Buffer
		code := Run(context.Background(), args, strings.NewReader(""), &output, io.Discard)
		if args[len(args)-1] == "--help" {
			if code != 0 {
				t.Fatalf("help: %d %s", code, &output)
			}
		} else if code != 2 {
			t.Fatalf("usage: %d %s", code, &output)
		}
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("usage contacted Herdr: %v", err)
	}
	root := shortTaskRoot(t)
	serveControlFixture(t, root, executable)
	t.Setenv("TEST_CONTROL_START", "1")
	t.Setenv("TEST_CONTROL_FAIL_VERIFY", "1")
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{
			"--root",
			root,
			"control",
			"--session",
			"work",
			"start",
			"--pane",
			"pane-1",
			"--name",
			"worker",
			"--kind",
			"codex",
		},
		strings.NewReader(""),
		&output,
		io.Discard,
	)
	if code != 1 || !strings.Contains(output.String(), "error:") ||
		strings.Contains(output.String(), "pane_id:") {
		t.Fatalf("verification: %d %s", code, &output)
	}
	t.Setenv("TEST_CONTROL_FAIL_VERIFY", "")
	if code := Run(context.Background(), []string{"control", "status", "worker"}, strings.NewReader(""), failedOutput{}, io.Discard); code != 1 {
		t.Fatalf("writer: %d", code)
	}
}
