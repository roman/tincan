package hostservice

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
)

func TestControlResponseOmitsNativeSessionMetadata(t *testing.T) {
	agent := herdr.Agent{PaneID: "pane-1", Kind: "codex", Status: herdr.Idle,
		Session: &herdr.SessionReference{
			Source: "herdr:codex",
			Agent:  "codex",
			Kind:   "id",
			Value:  "private-native-session",
		}}
	response := Response{
		Outcome: Completed,
		Control: &ControlResult{Target: "pane-1", Agent: agent},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("Session")) ||
		bytes.Contains(data, []byte("private-native-session")) {
		t.Fatalf("control response exposed internal metadata: %s", data)
	}
}

func TestControlResponsesRequireVerifiedEndpoint(t *testing.T) {
	start := Request{
		Operation: ControlStart,
		Control:   &ControlRequest{Target: "pane-1", Name: "worker", Kind: "codex"},
	}
	interrupt := Request{Operation: ControlInterrupt, Control: &ControlRequest{Target: "worker"}}
	for _, test := range []struct {
		name    string
		request Request
		result  ControlResult
		valid   bool
	}{
		{"start", start, ControlResult{Target: "pane-1", Agent: herdr.Agent{PaneID: "pane-1", Kind: "codex", Status: herdr.Idle}}, true},
		{"interactive codex", start, ControlResult{Target: "pane-1", Agent: herdr.Agent{PaneID: "pane-1", Kind: "codex", Status: herdr.Unknown, InteractiveReady: true}}, true},
		{"wrong pane", start, ControlResult{Target: "pane-1", Agent: herdr.Agent{PaneID: "other", Kind: "codex", Status: herdr.Idle}}, false},
		{"wrong kind", start, ControlResult{Target: "pane-1", Agent: herdr.Agent{PaneID: "pane-1", Kind: "claude", Status: herdr.Idle}}, false},
		{"unknown start", start, ControlResult{Target: "pane-1", Agent: herdr.Agent{PaneID: "pane-1", Kind: "codex", Status: herdr.Unknown}}, false},
		{"interrupt", interrupt, ControlResult{Target: "worker", Agent: herdr.Agent{PaneID: "pane-1", Kind: "codex", Status: herdr.Done}}, true},
		{"unsettled interrupt", interrupt, ControlResult{Target: "worker", Agent: herdr.Agent{PaneID: "pane-1", Kind: "codex", Status: herdr.Working}}, false},
		{"unknown interrupt", interrupt, ControlResult{Target: "worker", Agent: herdr.Agent{PaneID: "pane-1", Kind: "codex", Status: herdr.Unknown, InteractiveReady: true}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateResponse(
				Response{Outcome: Completed, Control: &test.result},
				test.request,
			)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
	result := &ControlResult{
		Target: "pane-1",
		Agent:  herdr.Agent{PaneID: "pane-1", Kind: "codex", Status: herdr.Idle},
	}
	for _, response := range []Response{
		{Outcome: Completed},
		{Outcome: Completed, Control: result, Task: &TaskResult{}},
		{Outcome: Failed, Error: "failed", Control: result},
		{Outcome: Uncertain, Error: "uncertain", Control: result},
	} {
		if err := validateResponse(response, start); err == nil {
			t.Fatalf("accepted invalid control response: %+v", response)
		}
	}
}
