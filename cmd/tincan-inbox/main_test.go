package main

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
)

func TestWaitExitCodeContract(t *testing.T) {
	for _, test := range []struct {
		name     string
		pending  bool
		timeout  string
		wantCode int
		wantLine string
	}{
		{name: "pending", pending: true, timeout: "10ms", wantLine: "status: pending"},
		{name: "checkpoint", timeout: "10ms", wantLine: "status: checkpoint"},
		{name: "invalid timeout", timeout: "0s", wantCode: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.pending {
				store, err := inbox.New(root)
				if err != nil {
					t.Fatal(err)
				}
				recipient, err := inbox.ParseRecipient("firstmate")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Send(recipient, "reply-1", "Worker reply"); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(os.Args[0], "-test.run=^TestInboxCommandHelper$", "--",
				"--root", root, "wait", "firstmate", "--timeout", test.timeout,
				"--interval", "1ms")
			command.Env = append(os.Environ(), "TINCAN_INBOX_HELPER=1")
			output, err := command.CombinedOutput()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != test.wantCode {
				t.Fatalf("exit code = %d, output = %q; want %d", code, output, test.wantCode)
			}
			if test.wantLine != "" {
				captured := strings.TrimRight(string(output), "\n")
				if !strings.HasPrefix(captured, test.wantLine+"\n") {
					t.Fatalf(
						"hook-visible output = %q; want prefix %q",
						captured,
						test.wantLine+"\n",
					)
				}
			}
		})
	}
}

func TestInboxCommandHelper(t *testing.T) {
	if os.Getenv("TINCAN_INBOX_HELPER") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		t.Fatal("missing helper argument separator")
	}
	os.Args = append([]string{"tincan-inbox"}, os.Args[separator+1:]...)
	main()
}
