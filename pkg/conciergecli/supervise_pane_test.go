package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSupervisePanePreservesManualRestartAndAmbientRouting(t *testing.T) {
	_, record := controlFixture(t)
	t.Setenv("TEST_CONTROL_START", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var output bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- Run(ctx, []string{"--root", "unused-relative", "--message-root", "unused-relative", "--recipient", "../unused", "supervise", "--interval", "1h", "pane", "--pane", "pane-1", "--name", "worker", "--kind", "codex", "--agent-arg", "resume", "--agent-arg", "thread,one", "--agent-arg=--flag"}, strings.NewReader(""), &output, io.Discard)
	}()
	want := "ambient|ambient-socket|ambient-pane|agent get worker\n" +
		"ambient|ambient-socket|ambient-pane|agent get pane-1\n" +
		"ambient|ambient-socket|ambient-pane|agent start worker --kind codex --pane pane-1 -- resume thread,one --flag\n" +
		"ambient|ambient-socket|ambient-pane|agent get worker\n"
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		calls, _ := os.ReadFile(record)
		if strings.HasPrefix(string(calls), want) {
			cancel()
			break
		}
		select {
		case code := <-result:
			t.Fatalf("supervision ended before restart: %d %s calls=%s", code, &output, calls)
		case <-ctx.Done():
			t.Fatalf("restart did not complete: %s", calls)
		case <-ticker.C:
		}
	}
	select {
	case code := <-result:
		if code != 0 || output.Len() != 0 {
			t.Fatalf("cancel: %d %s", code, &output)
		}
	case <-time.After(time.Second):
		t.Fatal("manual supervisor ignored cancellation")
	}
}

func TestSupervisePaneRejectsConfigurationBeforeHerdr(t *testing.T) {
	_, record := controlFixture(t)
	base := []string{"supervise", "pane", "--pane", "pane-1", "--name", "worker", "--kind", "codex"}
	for _, flags := range [][]string{{"--interval", "0s"}, {"--restart-window", "0s"}, {"--restart-tolerance", "-1"}, {"--restart-tolerance", "4294967296"}} {
		var output bytes.Buffer
		code := Run(
			context.Background(),
			append(append([]string{}, base...), flags...),
			strings.NewReader(""),
			&output,
			io.Discard,
		)
		if code != 2 || !strings.Contains(output.String(), "error:") {
			t.Fatalf("config: %v code%d %s", flags, code, &output)
		}
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("invalid config contacted Herdr: %v", err)
	}
	for _, args := range [][]string{{"supervise"}, {"supervise", "pane", "--help"}, append(base, "--unknown")} {
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
}

func TestSupervisePaneReportsBindingMismatch(t *testing.T) {
	for _, binding := range []struct{ name, pane, kind string }{{"pane", "different-pane", "codex"}, {"kind", "pane-1", "claude"}} {
		t.Run(binding.name, func(t *testing.T) {
			_, record := controlFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var output bytes.Buffer
			code := Run(
				ctx,
				[]string{
					"supervise",
					"--restart-tolerance",
					"0",
					"pane",
					"--pane",
					binding.pane,
					"--name",
					"worker",
					"--kind",
					binding.kind,
				},
				strings.NewReader(""),
				&output,
				io.Discard,
			)
			if code != 1 || !strings.Contains(output.String(), "error:") {
				t.Fatalf("binding: %d %s", code, &output)
			}
			calls, err := os.ReadFile(record)
			if err != nil ||
				string(calls) != "ambient|ambient-socket|ambient-pane|agent get worker\n" {
				t.Fatalf("unsafe restart attempt: %s %v", calls, err)
			}
		})
	}
}
