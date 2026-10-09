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
	if !healthAtom(paneID, true) {
		return "", fmt.Errorf("%w: invalid Herdr pane", ErrResponse)
	}
	data, err := client.run(ctx, "pane", "get", paneID)
	if err != nil {
		return "", err
	}
	var response struct {
		Result struct {
			Pane struct {
				PaneID        string `json:"pane_id"`
				ForegroundCWD string `json:"foreground_cwd"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("%w: %v", ErrResponse, err)
	}
	if response.Result.Pane.PaneID != paneID {
		return "", fmt.Errorf("%w: pane identity mismatch", ErrResponse)
	}
	path := response.Result.Pane.ForegroundCWD
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%w: missing or invalid foreground directory", ErrResponse)
	}
	return path, nil
}
