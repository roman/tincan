package conciergecli

import (
	"bytes"
	"context"
	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSupervisePanePreservesManualRestartAndHostRouting(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	executable, record := controlFixture(t)
	t.Setenv("TEST_CONTROL_START", "1")
	root := shortTaskRoot(t)
	claimNativeParent(t, root)
	recipient, _ := inbox.ParseRecipient("concierge")
	serveConfiguredTaskRoot(
		t,
		hostservice.Config{
			Root:            root,
			MessageRoot:     filepath.Join(root, "messages"),
			Recipient:       recipient,
			HerdrExecutable: executable,
			Session:         "ambient",
		},
	)
	args := []string{
		"supervise",
		"pane",
		"--pane",
		"pane-1",
		"--name",
		"worker",
		"--kind",
		"codex",
		"--agent-arg",
		"resume",
		"--agent-arg",
		"thread,one",
		"--agent-arg=--flag",
	}
	for range 2 {
		if got := invoke(t, root, "", args...); got != "pane: \"pane-1\"\nsupervision: active\n" {
			t.Fatal(got)
		}
	}
	calls, _ := os.ReadFile(record)
	want := "ambient|||agent get worker\nambient|||agent get pane-1\nambient|||agent start worker --kind codex --pane pane-1 -- resume thread,one --flag\nambient|||agent get worker\n"
	if !strings.HasPrefix(string(calls), want) {
		t.Fatalf("restart routing: %s", calls)
	}
	if got := invoke(t, root, "", "supervise", "stop", "pane", "--pane", "pane-1"); got != "pane: \"pane-1\"\nsupervision: stopped\n" {
		t.Fatal(got)
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
	if runUnderNativeParent(t) {
		return
	}
	for _, binding := range []struct{ name, pane, kind string }{{"pane", "different-pane", "codex"}, {"kind", "pane-1", "claude"}} {
		t.Run(binding.name, func(t *testing.T) {
			executable, record := controlFixture(t)
			root := shortTaskRoot(t)
			claimNativeParent(t, root)
			recipient, _ := inbox.ParseRecipient("concierge")
			serveConfiguredTaskRoot(
				t,
				hostservice.Config{
					Root:            root,
					MessageRoot:     filepath.Join(root, "messages"),
					Recipient:       recipient,
					HerdrExecutable: executable,
					Session:         "ambient",
				},
			)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var output bytes.Buffer
			code := Run(
				ctx,
				[]string{
					"--root", root,
					"supervise",
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
				string(calls) != "ambient|||agent get worker\n" {
				t.Fatalf("unsafe restart attempt: %s %v", calls, err)
			}
		})
	}
}
