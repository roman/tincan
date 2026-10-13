package conciergecli

import (
	"bytes"
	"context"
	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestControlMutationsRequireHostAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		for _, action := range []string{"start", "interrupt"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				executable, calls := controlFixture(t)
				root := shortTaskRoot(t)
				if mode == "unauthorized" {
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
				}
				args := []string{"--root", root, "control", action}
				if action == "start" {
					t.Setenv("TEST_CONTROL_START", "1")
					args = append(args, "--pane", "pane-1", "--name", "worker", "--kind", "codex")
				} else {
					t.Setenv("TEST_CONTROL_BUSY", "1")
					args = append(args, "worker")
				}
				var output bytes.Buffer
				code := Run(context.Background(), args, nil, &output, io.Discard)
				if code != 1 {
					t.Fatalf("unadmitted control code %d: %s", code, &output)
				}
				if _, err := os.Stat(calls); !os.IsNotExist(err) {
					t.Fatalf("unadmitted control called Herdr: %v", err)
				}
			})
		}
	}
}

func serveControlFixture(t *testing.T, root, executable string) {
	t.Helper()
	recipient, _ := inbox.ParseRecipient("concierge")
	claimNativeParent(t, root)
	serveConfiguredTaskRoot(
		t,
		hostservice.Config{
			Root:            root,
			MessageRoot:     filepath.Join(root, "messages"),
			Recipient:       recipient,
			HerdrExecutable: executable,
			Session:         "work",
		},
	)
}

func TestControlRejectsConflictingAmbientSession(t *testing.T) {
	if runUnderNativeParent(t) {
		return
	}
	for _, action := range []string{"start", "interrupt"} {
		t.Run(action, func(t *testing.T) {
			executable, calls := controlFixture(t)
			root := shortTaskRoot(t)
			serveControlFixture(t, root, executable)
			args := []string{"--root", root, "control", action}
			if action == "start" {
				t.Setenv("TEST_CONTROL_START", "1")
				args = append(args, "--pane", "pane-1", "--name", "worker", "--kind", "codex")
			} else {
				args = append(args, "worker")
			}
			var output bytes.Buffer
			code := Run(context.Background(), args, nil, &output, io.Discard)
			if code != 2 {
				t.Fatalf("conflicting session code %d: %s", code, &output)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatalf("conflicting session called Herdr: %v", err)
			}
		})
	}
}
