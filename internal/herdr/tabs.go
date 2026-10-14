package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

type TabInput struct {
	Workspace   string
	Directory   string
	Label       string
	Environment []string
}

type tabLocation struct {
	Workspace string `json:"workspace_id"`
	Tab       string `json:"tab_id"`
	Label     string `json:"label"`
}

func validateTabIdentity(workspace, label string) error {
	if !healthAtom(workspace, false) || !healthAtom(label, false) {
		return fmt.Errorf("%w: explicit workspace and unique tab label are required", ErrResponse)
	}
	return nil
}

func (client Client) CreateTab(ctx context.Context, input TabInput) (PaneDetails, error) {
	if err := validateTabIdentity(input.Workspace, input.Label); err != nil {
		return PaneDetails{}, err
	}
	options, err := creationOptions(input.Directory, input.Label, input.Environment)
	if err != nil {
		return PaneDetails{}, err
	}
	arguments := append([]string{"tab", "create", "--workspace", input.Workspace}, options...)
	output, err := client.run(ctx, arguments...)
	if err != nil {
		return PaneDetails{}, err
	}
	var response struct {
		Result struct {
			Tab  tabLocation `json:"tab"`
			Pane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return PaneDetails{}, fmt.Errorf("%w: %v", ErrResponse, err)
	}
	tab := response.Result.Tab
	if tab.Workspace != input.Workspace || tab.Label != input.Label || !healthAtom(tab.Tab, true) {
		return PaneDetails{}, fmt.Errorf(
			"%w: created tab identity differs from request",
			ErrResponse,
		)
	}
	return client.tabPane(ctx, tab, response.Result.Pane.ID)
}

// FindTab discovers a possible earlier creation without submitting another one.
func (client Client) FindTab(ctx context.Context, workspace, label string) (PaneDetails, error) {
	if err := validateTabIdentity(workspace, label); err != nil {
		return PaneDetails{}, err
	}
	output, err := client.run(ctx, "tab", "list", "--workspace", workspace)
	if err != nil {
		return PaneDetails{}, err
	}
	var tabs struct {
		Result struct {
			Tabs *[]tabLocation `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &tabs); err != nil || tabs.Result.Tabs == nil {
		return PaneDetails{}, fmt.Errorf("%w: invalid tab inventory", ErrResponse)
	}
	var match *tabLocation
	for _, tab := range *tabs.Result.Tabs {
		if tab.Label != label {
			continue
		}
		if match != nil || tab.Workspace != workspace || !healthAtom(tab.Tab, true) {
			return PaneDetails{}, fmt.Errorf(
				"%w: tab label has ambiguous or invalid matches",
				ErrResponse,
			)
		}
		copy := tab
		match = &copy
	}
	if match == nil {
		return PaneDetails{}, fmt.Errorf("%w: no tab has the reserved label", ErrNotFound)
	}
	output, err = client.run(ctx, "pane", "list", "--workspace", workspace)
	if err != nil {
		return PaneDetails{}, err
	}
	var panes struct {
		Result struct {
			Panes *[]struct {
				Workspace string `json:"workspace_id"`
				Tab       string `json:"tab_id"`
				Pane      string `json:"pane_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &panes); err != nil || panes.Result.Panes == nil {
		return PaneDetails{}, fmt.Errorf("%w: invalid pane inventory", ErrResponse)
	}
	paneID := ""
	for _, pane := range *panes.Result.Panes {
		if pane.Tab != match.Tab {
			continue
		}
		if paneID != "" || pane.Workspace != workspace || !healthAtom(pane.Pane, true) {
			return PaneDetails{}, fmt.Errorf(
				"%w: recovered tab must have exactly one matching pane",
				ErrResponse,
			)
		}
		paneID = pane.Pane
	}
	if paneID == "" {
		return PaneDetails{}, fmt.Errorf("%w: recovered tab has no pane", ErrResponse)
	}
	return client.tabPane(ctx, *match, paneID)
}

func (client Client) tabPane(
	ctx context.Context,
	tab tabLocation,
	paneID string,
) (PaneDetails, error) {
	details, err := client.PaneDetails(ctx, paneID)
	if err != nil {
		return PaneDetails{}, err
	}
	if details.WorkspaceID != tab.Workspace || details.TabID != tab.Tab {
		return PaneDetails{}, fmt.Errorf("%w: pane location changed from its tab", ErrResponse)
	}
	return details, nil
}

func creationOptions(directory, label string, environment []string) ([]string, error) {
	if !healthAtom(label, false) {
		return nil, ErrResponse
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory ||
		strings.IndexFunc(directory, unicode.IsControl) >= 0 {
		return nil, fmt.Errorf(
			"%w: a clean absolute tab directory is required",
			ErrResponse,
		)
	}
	arguments := []string{"--cwd", directory, "--label", label, "--no-focus"}
	seen := make(map[string]bool)
	for _, assignment := range environment {
		key, _, present := strings.Cut(assignment, "=")
		if !present || key == "" || seen[key] ||
			strings.IndexFunc(assignment, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf(
				"%w: invalid or duplicate tab environment assignment",
				ErrResponse,
			)
		}
		for index, character := range key {
			if character != '_' && (character < 'A' || character > 'Z') &&
				(character < 'a' || character > 'z') && (index == 0 || character < '0' || character > '9') {
				return nil, fmt.Errorf("%w: invalid tab environment name", ErrResponse)
			}
		}
		seen[key] = true
		arguments = append(arguments, "--env", assignment)
	}
	return arguments, nil
}
