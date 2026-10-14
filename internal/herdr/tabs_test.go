package herdr

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const launchPaneJSON = `{"result":{"pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t2","foreground_cwd":"/worker"}}}`

func TestCreateTabUsesExplicitWorkspaceAndReturnedLocation(t *testing.T) {
	runner := &fakeRunner{responses: []runResponse{
		{
			stdout: []byte(
				`{"result":{"tab":{"workspace_id":"w1","tab_id":"w1:t2","label":"launch-one"},"root_pane":{"pane_id":"w1:p2"}}}`,
			),
		},
		{stdout: []byte(launchPaneJSON)},
	}}
	client := Client{Runner: runner}
	got, err := client.CreateTab(context.Background(), TabInput{
		Workspace: "w1", Directory: "/worker", Label: "launch-one",
		Environment: []string{"CONCIERGE_WORKER=1", "TINCAN_HOME=/messages"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != (PaneDetails{PaneID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t2", ForegroundDirectory: "/worker"}) {
		t.Fatalf("location = %+v", got)
	}
	want := [][]string{
		{
			"tab",
			"create",
			"--workspace",
			"w1",
			"--cwd",
			"/worker",
			"--label",
			"launch-one",
			"--no-focus",
			"--env",
			"CONCIERGE_WORKER=1",
			"--env",
			"TINCAN_HOME=/messages",
		},
		{"pane", "get", "w1:p2"},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestFindTabRequiresUniqueLabelAndSinglePane(t *testing.T) {
	for _, test := range []struct {
		name, tabs, panes string
		wantError         error
	}{
		{name: "unique", tabs: `{"result":{"tabs":[{"workspace_id":"w1","tab_id":"w1:t2","label":"launch-one"}]}}`, panes: `{"result":{"panes":[{"workspace_id":"w1","tab_id":"w1:t2","pane_id":"w1:p2"},{"workspace_id":"w1","tab_id":"w1:t3","pane_id":"w1:p3"}]}}`},
		{name: "absent", tabs: `{"result":{"tabs":[]}}`, wantError: ErrNotFound},
		{name: "duplicate", tabs: `{"result":{"tabs":[{"workspace_id":"w1","tab_id":"w1:t2","label":"launch-one"},{"workspace_id":"w1","tab_id":"w1:t3","label":"launch-one"}]}}`, wantError: ErrResponse},
		{name: "split", tabs: `{"result":{"tabs":[{"workspace_id":"w1","tab_id":"w1:t2","label":"launch-one"}]}}`, panes: `{"result":{"panes":[{"workspace_id":"w1","tab_id":"w1:t2","pane_id":"w1:p2"},{"workspace_id":"w1","tab_id":"w1:t2","pane_id":"w1:p3"}]}}`, wantError: ErrResponse},
		{name: "wrong workspace", tabs: `{"result":{"tabs":[{"workspace_id":"w2","tab_id":"w1:t2","label":"launch-one"}]}}`, wantError: ErrResponse},
		{name: "malformed", tabs: `{"result":{}}`, wantError: ErrResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			responses := []runResponse{{stdout: []byte(test.tabs)}}
			if test.panes != "" {
				responses = append(
					responses,
					runResponse{stdout: []byte(test.panes)},
					runResponse{stdout: []byte(launchPaneJSON)},
				)
			}
			runner := &fakeRunner{responses: responses}
			got, err := (Client{Runner: runner}).FindTab(context.Background(), "w1", "launch-one")
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if err == nil && got.PaneID != "w1:p2" {
				t.Fatalf("pane = %+v", got)
			}
			for _, call := range runner.calls {
				if call[0] == "tab" && call[1] == "create" {
					t.Fatal("discovery created another tab")
				}
			}
		})
	}
}

func TestCreateTabRejectsInvalidInputBeforeEffects(t *testing.T) {
	for _, test := range []TabInput{
		{Directory: "/worker", Label: "launch-one"},
		{Workspace: "w1", Directory: "relative", Label: "launch-one"},
		{Workspace: "w1", Directory: "/worker", Label: "bad\nlabel"},
		{Workspace: "w1", Directory: "/worker", Label: "launch-one", Environment: []string{"INVALID"}},
		{Workspace: "w1", Directory: "/worker", Label: "launch-one", Environment: []string{"A=1", "A=2"}},
	} {
		runner := &fakeRunner{}
		if _, err := (Client{Runner: runner}).CreateTab(context.Background(), test); err == nil {
			t.Fatalf("invalid input accepted: %+v", test)
		}
		if len(runner.calls) != 0 {
			t.Fatal("invalid input reached Herdr")
		}
	}
}
