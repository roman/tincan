package conciergecli

import (
	"bytes"
	"context"
	"github.com/roman/tincan/pkg/hostservice"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
)

func TestAssignmentSendRequiresHostAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			root, messageRoot, prompts := assignmentSendFixture(t)
			if mode == "unauthorized" {
				executable, err := exec.LookPath("herdr")
				if err != nil {
					t.Fatal(err)
				}
				recipient, _ := inbox.ParseRecipient("concierge")
				serveConfiguredTaskRoot(
					t,
					hostservice.Config{
						Root:            root,
						MessageRoot:     messageRoot,
						Recipient:       recipient,
						HerdrExecutable: executable,
					},
				)
			}
			var output bytes.Buffer
			code := Run(
				context.Background(),
				[]string{
					"--root",
					root,
					"--message-root",
					messageRoot,
					"task",
					"send",
					"task-1",
					"--key",
					"brief",
					"--body-file",
					"-",
				},
				strings.NewReader("Work"),
				&output,
				io.Discard,
			)
			if code != 1 {
				t.Fatalf("unadmitted send code %d: %s", code, &output)
			}
			messages, err := inbox.New(messageRoot)
			if err != nil {
				t.Fatal(err)
			}
			recipient, _ := inbox.ParseRecipient("task-1")
			pending, err := messages.List(recipient)
			if err != nil || len(pending) != 0 {
				t.Fatalf("unadmitted send published messages: %v %v", pending, err)
			}
			if _, err := os.Stat(prompts); !os.IsNotExist(err) {
				t.Fatalf("unadmitted send prompted worker: %v", err)
			}
		})
	}
}
