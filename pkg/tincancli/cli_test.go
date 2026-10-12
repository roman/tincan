package tincancli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
)

func TestInboxLifecycleAndOutput(t *testing.T) {
	root := t.TempDir()
	invoke := func(input string, args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args = append([]string{"--root", root, "inbox"}, args...)
		if code := Run(context.Background(), args, strings.NewReader(input), &stdout, &stderr); code != 0 {
			t.Fatalf("%v: code%d %s", args, code, &stderr)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr: %s", &stderr)
		}
		return stdout.String()
	}
	sent := invoke(
		"first\nsecond, \"quoted\"",
		"send",
		"worker",
		"--key",
		"retry,1",
		"--body-file",
		"-",
	)
	store, err := inbox.New(root)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("worker")
	messages, err := store.List(recipient)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages: %v %v", messages, err)
	}
	message := messages[0]
	if want := fmt.Sprintf("id: %s\nrecipient: worker\n", message.ID); sent != want {
		t.Fatalf("send=%q", sent)
	}
	timestamp := message.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
	if got, want := invoke("", "list", "worker"), fmt.Sprintf("count: 1\nmessages[1]{id,key,created_at}:\n  %s,\"retry,1\",%s\n", message.ID, timestamp); got != want {
		t.Fatalf("list=%q want%q", got, want)
	}
	if got, want := invoke("", "read", "worker", message.ID.String()), fmt.Sprintf("id: %s\nrecipient: worker\nkey: \"retry,1\"\nbody: \"first\\nsecond, \\\"quoted\\\"\"\ncreated_at: %s\n", message.ID, timestamp); got != want {
		t.Fatalf("read=%q want%q", got, want)
	}
	for range 2 {
		if got := invoke("", "ack", "worker", message.ID.String()); got != "acknowledged: "+message.ID.String()+"\n" {
			t.Fatalf("ack=%q", got)
		}
	}
	if got := invoke("", "list", "worker"); got != "count: 0\n" {
		t.Fatalf("empty=%q", got)
	}
	if got := invoke("first\nsecond, \"quoted\"", "send", "worker", "--key", "retry,1", "--body-file", "-"); got != sent {
		t.Fatalf("retry=%q", got)
	}
	if got := invoke("", "list", "worker"); got != "count: 0\n" {
		t.Fatalf("handled retry=%q", got)
	}
}

func TestMergedInboxRootPrecedence(t *testing.T) {
	for _, name := range []string{"explicit", "environment", "default", "relative"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			t.Chdir(directory)
			t.Setenv("TINCAN_HOME", "")
			root := filepath.Join(directory, ".tincan")
			args := []string{"inbox", "send", "worker", "--body", "work"}
			switch name {
			case "explicit":
				t.Setenv("TINCAN_HOME", filepath.Join(directory, "unused"))
				root = filepath.Join(directory, "chosen")
				args = append([]string{"--root", root}, args...)
			case "environment":
				root = filepath.Join(directory, "environment")
				t.Setenv("TINCAN_HOME", root)
			case "relative":
				root = filepath.Join(directory, "relative")
				args = append([]string{"--root", "relative"}, args...)
			}
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d %s", code, &stderr)
			}
			store, _ := inbox.New(root)
			recipient, _ := inbox.ParseRecipient("worker")
			messages, err := store.List(recipient)
			if err != nil || len(messages) != 1 {
				t.Fatalf("root=%s messages=%v err=%v", root, messages, err)
			}
			if _, err := os.Stat(filepath.Join(directory, "unused")); !os.IsNotExist(err) {
				t.Fatalf("unused root: %v", err)
			}
		})
	}
}

func TestMergedInboxRejectsInputBeforeStorage(t *testing.T) {
	for _, trial := range []struct {
		name string
		args []string
		code int
	}{
		{"unknown", []string{"inbox", "send", "worker", "--body", "work", "--typo"}, 2},
		{"missing recipient", []string{"inbox", "send", "--body", "work"}, 2},
		{"missing body", []string{"inbox", "send", "worker"}, 1},
		{"ambiguous body", []string{"inbox", "send", "worker", "--body", "work", "--body-file", "missing"}, 1},
		{"bad recipient", []string{"inbox", "send", "../worker", "--body", "work"}, 1},
		{"bad duration", []string{"inbox", "wait", "worker", "--timeout", "0s"}, 1},
	} {
		t.Run(trial.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent")
			var stdout, stderr bytes.Buffer
			args := append([]string{"--root", root}, trial.args...)
			if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != trial.code ||
				stderr.Len() == 0 ||
				stdout.Len() != 0 {
				t.Fatalf("code%d stdout%s stderr%s", code, &stdout, &stderr)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("state created: %v", err)
			}
		})
	}
}

func TestMergedInboxHelpAndVersionAvoidRootDiscovery(t *testing.T) {
	t.Setenv("TINCAN_HOME", "")
	directory := t.TempDir()
	t.Chdir(directory)
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Fatal("removed current directory remains discoverable")
	}
	for _, args := range [][]string{{"--help"}, {"notify", "--help"}, {"inbox", "--help"}, {"inbox", "send", "--help"}, {"--version"}, {"-v"}, {"-V"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 ||
			stdout.Len() == 0 ||
			stderr.Len() != 0 {
			t.Fatalf("%v code%d stdout%s stderr%s", args, code, &stdout, &stderr)
		}
		if len(args) == 1 && args[0] != "--help" && stdout.String() != "0.1.0\n" {
			t.Fatalf("version=%q", stdout.String())
		}
	}
}
