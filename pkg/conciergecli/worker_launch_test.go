package conciergecli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type launchBodyReader struct{ reads int }

func (reader *launchBodyReader) Read([]byte) (int, error) {
	reader.reads++
	return 0, errors.New("unexpected body read")
}

func TestWorkerMessagesRejectMissingOrMismatchedLaunchBeforeIO(t *testing.T) {
	for _, operation := range []string{"receive", "ack", "reply"} {
		for _, test := range []struct{ name, launch, generation string }{
			{"missing", "", "gen-1"},
			{"mismatch", "gen-old", "gen-1"},
			{"malformed", "bad generation", "bad generation"},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				t.Setenv("CONCIERGE_SPAWN_GENERATION", test.launch)
				root := filepath.Join(t.TempDir(), "state")
				messages := filepath.Join(t.TempDir(), "messages")
				args := []string{"--root", root, "--message-root", messages, "task", operation}
				if operation == "reply" {
					args = append(args, "send", "task-1", "--key", "result", "--body-file", "-")
				} else {
					args = append(args, "task-1", "m-"+strings.Repeat("0", 32))
				}
				args = append(args, "--generation", test.generation)
				input := &launchBodyReader{}
				var output bytes.Buffer
				code := Run(context.Background(), args, input, &output, io.Discard)
				if code != 1 || !strings.Contains(output.String(), "CONCIERGE_SPAWN_GENERATION") ||
					!strings.Contains(output.String(), "controlled worker relaunch") {
					t.Fatalf("launch rejection: %d %s", code, &output)
				}
				if input.reads != 0 {
					t.Fatalf("read body before launch validation: %d", input.reads)
				}
				for _, path := range []string{root, messages} {
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("created storage before launch validation: %s %v", path, err)
					}
				}
			})
		}
	}
}
