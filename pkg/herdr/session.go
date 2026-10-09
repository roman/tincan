package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

type boundSessionRunner struct {
	session string
	runner  SessionRunner
}

// PaneDetails describes the current Herdr location and foreground directory.
type PaneDetails struct {
	PaneID              string
	WorkspaceID         string
	TabID               string
	ForegroundDirectory string
}

func (runner boundSessionRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	return runner.runner.RunSession(ctx, runner.session, args...)
}

// ForSession routes all ordinary agent and pane commands through one explicit Herdr session.
func (client Client) ForSession(session string) (Client, error) {
	if !healthAtom(session, false) {
		return Client{}, fmt.Errorf("%w: invalid Herdr session", ErrResponse)
	}
	if client.SessionRunner == nil {
		return Client{}, fmt.Errorf("%w: no session runner", ErrCommand)
	}
	client.Runner = boundSessionRunner{session: session, runner: client.SessionRunner}
	return client, nil
}

// ForegroundDirectory returns the live foreground process directory for a pane.
// The pane's creation-time cwd is intentionally ignored.
func (client Client) ForegroundDirectory(ctx context.Context, paneID string) (string, error) {
	details, err := client.readPane(ctx, paneID)
	return details.ForegroundDirectory, err
}

// PaneDetails reads the live location needed to bind a local task to a pane.
func (client Client) PaneDetails(ctx context.Context, paneID string) (PaneDetails, error) {
	details, err := client.readPane(ctx, paneID)
	if err != nil {
		return PaneDetails{}, err
	}
	if !healthAtom(details.WorkspaceID, false) || !healthAtom(details.TabID, true) {
		return PaneDetails{}, fmt.Errorf("%w: missing or invalid pane location", ErrResponse)
	}
	return details, nil
}

func (client Client) readPane(ctx context.Context, paneID string) (PaneDetails, error) {
	if !healthAtom(paneID, true) {
		return PaneDetails{}, fmt.Errorf("%w: invalid Herdr pane", ErrResponse)
	}
	data, err := client.run(ctx, "pane", "get", paneID)
	if err != nil {
		return PaneDetails{}, err
	}
	var response struct {
		Result struct {
			Pane struct {
				PaneID        string `json:"pane_id"`
				ForegroundCWD string `json:"foreground_cwd"`
				WorkspaceID   string `json:"workspace_id"`
				TabID         string `json:"tab_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return PaneDetails{}, fmt.Errorf("%w: %v", ErrResponse, err)
	}
	if response.Result.Pane.PaneID != paneID {
		return PaneDetails{}, fmt.Errorf("%w: pane identity mismatch", ErrResponse)
	}
	path := response.Result.Pane.ForegroundCWD
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return PaneDetails{}, fmt.Errorf("%w: missing or invalid foreground directory", ErrResponse)
	}
	return PaneDetails{
		PaneID: paneID, WorkspaceID: response.Result.Pane.WorkspaceID,
		TabID: response.Result.Pane.TabID, ForegroundDirectory: path,
	}, nil
}
