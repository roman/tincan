package tincancli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
)

func TestMergedNotifyUsesSelectedSessionAndKeepsMessage(t *testing.T) {
	for _, session := range []string{"ambient", "explicit"} {
		t.Run(session, func(t *testing.T) {
			directory := t.TempDir()
			capture := filepath.Join(directory, "call")
			executable := filepath.Join(directory, "herdr")
			program := "#!/bin/sh\nprintf '%s\\n' \"$HERDR_SESSION\" \"$@\" > \"$TINCAN_TEST_CAPTURE\"\nprintf '{}\\n'\n"
			if err := os.WriteFile(executable, []byte(program), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HERDR_SESSION", "ambient")
			t.Setenv("TINCAN_TEST_CAPTURE", capture)
			root := filepath.Join(directory, "messages")
			t.Setenv("TINCAN_HOME", root)
			store, _ := inbox.New(root)
			recipient, _ := inbox.ParseRecipient("worker")
			message, err := store.Send(recipient, "task", "Durable work")
			if err != nil {
				t.Fatal(err)
			}
			args := []string{
				"notify",
				"--recipient",
				"worker",
				"--target",
				"w1:p2",
				"--herdr",
				executable,
				"--interval",
				"1h",
			}
			if session == "explicit" {
				args = append(args, "--session", "explicit")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			if code := Run(ctx, args, strings.NewReader(""), &stdout, &stderr); code != 0 ||
				stdout.Len() != 0 ||
				stderr.Len() != 0 {
				t.Fatalf("code%d stdout%s stderr%s", code, &stdout, &stderr)
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			want := session + "\nagent\nprompt\nw1:p2\n" + notify.DoorbellText + "\n"
			if string(data) != want {
				t.Fatalf("Herdr call=%q want%q", data, want)
			}
			messages, err := store.List(recipient)
			if err != nil || len(messages) != 1 || messages[0] != message {
				t.Fatalf("pending=%v err=%v", messages, err)
			}
		})
	}
}

func TestMergedNotifyRejectsInputBeforeHerdr(t *testing.T) {
	for _, trial := range []struct {
		name  string
		flags []string
		code  int
	}{
		{"unknown flag", []string{"--typo"}, 2},
		{"bad interval", []string{"--interval", "0s"}, 1},
		{"unsupported adapter", []string{"--adapter", "tmux"}, 1},
		{"invalid target", []string{"--target", "worker\nother"}, 1},
		{"invalid recipient", []string{"--recipient", "../worker"}, 1},
	} {
		t.Run(trial.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent")
			args := []string{
				"--root",
				root,
				"notify",
				"--recipient",
				"worker",
				"--target",
				"worker",
				"--session",
				"work",
				"--herdr",
				"/nonexistent-herdr",
			}
			args = append(args, trial.flags...)
			var stdout, stderr bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if code := Run(ctx, args, strings.NewReader(""), &stdout, &stderr); code != trial.code ||
				stdout.Len() != 0 ||
				stderr.Len() == 0 {
				t.Fatalf("code%d stdout%s stderr%s", code, &stdout, &stderr)
			}
			if strings.Contains(stderr.String(), "/nonexistent-herdr") {
				t.Fatalf("Herdr called: %s", &stderr)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("state created: %v", err)
			}
		})
	}
}
