package hostservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManualStartAndSupervisionRejectTaskBoundPane(t *testing.T) {
	for _, operation := range []Operation{ControlStart, Supervise} {
		t.Run(string(operation), func(t *testing.T) {
			server, _, _ := taskEndpointFixture(t)
			server.config.Session = "work"
			calls := filepath.Join(t.TempDir(), "calls")
			if err := os.WriteFile(server.config.HerdrExecutable, []byte("#!/bin/sh\nprintf called > '"+calls+"'\nexit 72\n"), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == ControlStart {
				_, err = server.control(
					context.Background(),
					operation,
					ControlRequest{Target: "pane-1", Name: "worker", Kind: "codex"},
				)
			} else {
				_, err = server.supervision(context.Background(), operation, SupervisionRequest{Pane: &PaneSupervision{Pane: "pane-1", Name: "worker", Kind: "codex"}})
			}
			if err == nil || !strings.Contains(err.Error(), "task-bound") {
				t.Fatalf("task ownership: %v", err)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatalf("task-bound pane reached Herdr: %v", err)
			}
		})
	}
}

func TestControlInterruptResolvesAliasBeforeResidentReservation(t *testing.T) {
	server, _, endpoint := taskEndpointFixture(t)
	server.config.Session = "work"
	calls := filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\nprintf '%s\\n' '{\"result\":{\"agent\":{\"pane_id\":\"pane-1\",\"agent_status\":\"working\",\"agent\":\"codex\"}}}'\n"
	if err := os.WriteFile(server.config.HerdrExecutable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	startEndpointResident(
		t,
		server,
		controllerKey{kind: paneController, id: "pane-1"},
		endpoint,
		nil,
	)
	_, err := server.control(
		context.Background(),
		ControlInterrupt,
		ControlRequest{Target: "worker"},
	)
	if !errors.Is(err, errControllerBusy) {
		t.Fatalf("alias bypassed resident: %v", err)
	}
	data, _ := os.ReadFile(calls)
	if string(data) != "agent get worker\n" {
		t.Fatalf("reserved alias mutation: %s", data)
	}
}
