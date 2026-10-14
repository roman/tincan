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
