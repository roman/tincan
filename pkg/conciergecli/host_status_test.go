package conciergecli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskops"
)

func TestHostStatusMissingSocketIsUnavailable(t *testing.T) {
	root := shortTaskRoot(t)
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "host", "status", "--timeout", "50ms"},
		nil,
		&output,
		io.Discard,
	)
	if code != 1 || !strings.HasPrefix(output.String(), "status: unavailable\n") {
		t.Fatalf("missing host code %d: %s", code, &output)
	}
}

func TestHostStatusRefusedSocketIsUnavailable(t *testing.T) {
	root := shortTaskRoot(t)
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: filepath.Join(root, "host.sock"), Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "host", "status"},
		nil,
		&output,
		io.Discard,
	)
	if code != 1 || !strings.HasPrefix(output.String(), "status: unavailable\n") {
		t.Fatalf("refused host code %d: %s", code, &output)
	}
}

func TestHostStatusVerifiesPackageAndConfiguration(t *testing.T) {
	for _, scenario := range []string{"ready", "reuse session", "package", "message root", "recipient", "session", "Herdr environment", "Herdr flag"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("HERDR_SESSION", "")
			t.Setenv("TINCAN_HERDR", "")
			root := shortTaskRoot(t)
			messageRoot := filepath.Join(root, "messages")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			recipient, _ := inbox.ParseRecipient("concierge")
			config := hostservice.Config{
				Root:            root,
				MessageRoot:     messageRoot,
				Recipient:       recipient,
				Session:         "work",
				HerdrExecutable: unusedHerdr(t),
				WorkerCommands:  taskops.CommandPaths{Concierge: executable, Tincan: executable},
			}
			switch scenario {
			case "package":
				config.WorkerCommands.Concierge = "/different/concierge"
			case "message root":
				config.MessageRoot = filepath.Join(root, "different")
			case "recipient":
				config.Recipient, _ = inbox.ParseRecipient("different")
			case "session":
				config.Session = "different"
			}
			serveConfiguredTaskRoot(t, config)
			args := []string{"--root", root, "--message-root", messageRoot, "host"}
			if scenario == "Herdr environment" {
				t.Setenv("TINCAN_HERDR", unusedHerdr(t))
			}
			if scenario == "Herdr flag" {
				args = append(args, "--herdr", unusedHerdr(t))
			}
			if scenario != "reuse session" {
				args = append(args, "--session", "work")
			}
			args = append(args, "status")
			var output bytes.Buffer
			code := Run(context.Background(), args, nil, &output, io.Discard)
			if scenario == "ready" || scenario == "reuse session" {
				if code != 0 || !strings.HasPrefix(output.String(), "status: ready\n") ||
					!strings.Contains(output.String(), "session: \"work\"") {
					t.Fatalf("ready code %d: %s", code, &output)
				}
			} else if code != 2 || !strings.HasPrefix(output.String(), "status: incompatible\n") {
				t.Fatalf("incompatible code %d: %s", code, &output)
			}
		})
	}
}

func TestHostStatusTimesOutBeforeReadiness(t *testing.T) {
	root := shortTaskRoot(t)
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: filepath.Join(root, "host.sock"), Net: "unix"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	var output bytes.Buffer
	code := Run(
		ctx,
		[]string{"--root", root, "host", "status", "--timeout", "50ms"},
		nil,
		&output,
		io.Discard,
	)
	if code != 3 || !strings.HasPrefix(output.String(), "status: unresponsive\n") ||
		time.Since(started) > 2*time.Second {
		t.Fatalf("unresponsive code %d: %s", code, &output)
	}
}

func TestHostStatusDeniedSocketRequiresInspection(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can bypass socket mode permissions")
	}
	root := shortTaskRoot(t)
	path := filepath.Join(root, "host.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"--root", root, "host", "status"},
		nil,
		&output,
		io.Discard,
	)
	if code != 2 || strings.Contains(output.String(), "status: unavailable") {
		t.Fatalf("denied socket code %d: %s", code, &output)
	}
}

func TestHostStatusOldHostAndMalformedResponseRequireInspection(t *testing.T) {
	for _, scenario := range []string{"old host", "malformed response"} {
		t.Run(scenario, func(t *testing.T) {
			root := shortTaskRoot(t)
			listener, err := net.ListenUnix(
				"unix",
				&net.UnixAddr{Name: filepath.Join(root, "host.sock"), Net: "unix"},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				connection, err := listener.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer connection.Close()
				var request hostservice.Request
				if err := json.NewDecoder(connection).Decode(&request); err != nil {
					done <- err
					return
				}
				if scenario == "old host" {
					done <- json.NewEncoder(connection).Encode(hostservice.Response{Outcome: hostservice.Invalid, Error: "unknown field status"})
					return
				}
				_, err = io.WriteString(connection, "invalid response\n")
				done <- err
			}()
			var output bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			code := Run(ctx, []string{"--root", root, "host", "status"}, nil, &output, io.Discard)
			if code != 2 || strings.Contains(output.String(), "status: unavailable") {
				t.Fatalf("bad host code %d: %s", code, &output)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
