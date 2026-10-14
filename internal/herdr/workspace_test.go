package herdr

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestValidateWorkspaceRequiresMatchingID(t *testing.T) {
	for _, test := range []struct {
		name, input, output string
		wantError           error
	}{
		{"existing ID", "w19", `{"result":{"workspace":{"workspace_id":"w19","label":"review"}}}`, nil},
		{"label", "review", `{"result":{"workspace":{"workspace_id":"w19","label":"review"}}}`, ErrResponse},
		{"wrong ID", "w19", `{"result":{"workspace":{"workspace_id":"w20"}}}`, ErrResponse},
		{"missing ID", "w19", `{"result":{"workspace":{}}}`, ErrResponse},
		{"malformed", "w19", `{`, ErrResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{responses: []runResponse{{stdout: []byte(test.output)}}}
			err := (Client{Runner: runner}).ValidateWorkspace(context.Background(), test.input)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if !reflect.DeepEqual(runner.calls, [][]string{{"workspace", "get", test.input}}) {
				t.Fatalf("calls = %v", runner.calls)
			}
		})
	}
}

func TestMissingWorkspaceRetainsTypedError(t *testing.T) {
	response := runResponse{
		err: errors.New("exit status 1"),
		stderr: []byte(
			`{"error":{"code":"workspace_not_found","message":"workspace review-rcl not found"}}`,
		),
	}
	err := (Client{Runner: &fakeRunner{responses: []runResponse{response}}}).ValidateWorkspace(
		context.Background(),
		"review-rcl",
	)
	if !errors.Is(err, ErrWorkspaceNotFound) || !errors.Is(err, ErrCommand) {
		t.Fatalf("lost typed workspace error: %v", err)
	}
}

func TestAbandonmentRejectsOwnedWorkspaceWithoutRecordedPane(t *testing.T) {
	runner := &fakeRunner{responses: []runResponse{
		{stderr: []byte(`{"error":{"code":"agent_not_found"}}`), err: errors.New("exit 1")},
		{
			stdout: []byte(
				`{"result":{"workspaces":[{"workspace_id":"w1","label":"concierge-gen"}]}}`,
			),
		},
		{
			stdout: []byte(
				`{"result":{"tabs":[{"workspace_id":"w1","tab_id":"w1:t1","label":"default"}]}}`,
			),
		},
	}}
	err := (Client{Runner: runner}).CheckLaunchAbsent(
		context.Background(),
		LaunchResources{Name: "worker", Label: "concierge-gen"},
	)
	if err == nil {
		t.Fatal("abandoned an owned workspace after lost creation response")
	}
}

func TestFindWorkspaceRejectsAmbiguousRecovery(t *testing.T) {
	for _, test := range []struct{ name, workspaces, panes string }{
		{"missing", `[]`, ``},
		{"duplicate labels", `[{"workspace_id":"w1","label":"reserved"},{"workspace_id":"w2","label":"reserved"}]`, ``},
		{"added user pane", `[{"workspace_id":"w1","label":"reserved"}]`, `[{"workspace_id":"w1","tab_id":"w1:t1","pane_id":"w1:p1"},{"workspace_id":"w1","tab_id":"w1:t2","pane_id":"w1:p2"}]`},
		{"foreign pane", `[{"workspace_id":"w1","label":"reserved"}]`, `[{"workspace_id":"w2","tab_id":"w2:t1","pane_id":"w2:p1"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{
				responses: []runResponse{
					{stdout: []byte(`{"result":{"workspaces":` + test.workspaces + `}}`)},
					{stdout: []byte(`{"result":{"panes":` + test.panes + `}}`)},
				},
			}
			if _, err := (Client{Runner: runner}).FindWorkspace(context.Background(), "reserved"); err == nil {
				t.Fatal("ambiguous recovery accepted")
			}
			for _, call := range runner.calls {
				if len(call) > 1 && call[1] == "create" {
					t.Fatal("recovery created another workspace")
				}
			}
		})
	}
}

func TestCreateWorkspaceRejectsMismatchedCreationIdentity(t *testing.T) {
	for _, output := range []string{
		`{"result":{"workspace":{"workspace_id":"w1","label":"other"},"tab":{"workspace_id":"w1","tab_id":"w1:t1"},"root_pane":{"pane_id":"w1:p1"}}}`,
		`{"result":{"workspace":{"workspace_id":"w1","label":"reserved"},"tab":{"workspace_id":"w2","tab_id":"w2:t1"},"root_pane":{"pane_id":"w2:p1"}}}`,
	} {
		runner := &fakeRunner{responses: []runResponse{{stdout: []byte(output)}}}
		if _, err := (Client{Runner: runner}).CreateWorkspace(context.Background(), WorkspaceInput{Directory: "/tmp", Label: "reserved"}); !errors.Is(
			err,
			ErrResponse,
		) {
			t.Fatalf("creation identity: %v", err)
		}
	}
}
