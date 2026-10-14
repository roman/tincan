package herdr

import (
	"context"
	"encoding/json"
	"fmt"
)

// ValidateWorkspace requires an existing workspace ID in the selected session.
func (client Client) ValidateWorkspace(ctx context.Context, workspace string) error {
	if !healthAtom(workspace, false) {
		return fmt.Errorf("%w: invalid workspace ID", ErrResponse)
	}
	output, err := client.run(ctx, "workspace", "get", workspace)
	if err != nil {
		return fmt.Errorf("validate workspace ID %q: %w", workspace, err)
	}
	var response struct {
		Result struct {
			Workspace struct {
				ID string `json:"workspace_id"`
			} `json:"workspace"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return fmt.Errorf("%w: workspace lookup: %v", ErrResponse, err)
	}
	if response.Result.Workspace.ID != workspace {
		return fmt.Errorf("%w: provide an existing workspace ID", ErrResponse)
	}
	return nil
}

type WorkspaceInput struct {
	Directory   string
	Label       string
	Environment []string
}

// CreateWorkspace uses its initial pane as the worker endpoint.
func (client Client) CreateWorkspace(
	ctx context.Context,
	input WorkspaceInput,
) (PaneDetails, error) {
	options, err := creationOptions(input.Directory, input.Label, input.Environment)
	if err != nil {
		return PaneDetails{}, err
	}
	output, err := client.run(ctx, append([]string{"workspace", "create"}, options...)...)
	if err != nil {
		return PaneDetails{}, err
	}
	var response struct {
		Result struct {
			Workspace struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspace"`
			Tab  tabLocation `json:"tab"`
			Pane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if json.Unmarshal(output, &response) != nil {
		return PaneDetails{}, ErrResponse
	}
	workspace := response.Result.Workspace
	tab := response.Result.Tab
	if !healthAtom(workspace.ID, false) || workspace.Label != input.Label ||
		tab.Workspace != workspace.ID ||
		!healthAtom(tab.Tab, true) {
		return PaneDetails{}, ErrResponse
	}
	return client.tabPane(ctx, tab, response.Result.Pane.ID)
}

// FindWorkspace recovers a reserved creation without submitting another effect.
func (client Client) FindWorkspace(ctx context.Context, label string) (PaneDetails, error) {
	if !healthAtom(label, false) {
		return PaneDetails{}, ErrResponse
	}
	output, err := client.run(ctx, "workspace", "list")
	if err != nil {
		return PaneDetails{}, err
	}
	var response struct {
		Result struct {
			Workspaces *[]struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if json.Unmarshal(output, &response) != nil || response.Result.Workspaces == nil {
		return PaneDetails{}, ErrResponse
	}
	workspace := ""
	for _, candidate := range *response.Result.Workspaces {
		if candidate.Label != label {
			continue
		}
		if workspace != "" || !healthAtom(candidate.ID, false) {
			return PaneDetails{}, ErrResponse
		}
		workspace = candidate.ID
	}
	if workspace == "" {
		return PaneDetails{}, ErrWorkspaceNotFound
	}
	output, err = client.run(ctx, "pane", "list", "--workspace", workspace)
	if err != nil {
		return PaneDetails{}, err
	}
	var panes struct {
		Result struct {
			Panes *[]struct {
				ID        string `json:"pane_id"`
				Workspace string `json:"workspace_id"`
				Tab       string `json:"tab_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	if json.Unmarshal(output, &panes) != nil || panes.Result.Panes == nil ||
		len(*panes.Result.Panes) != 1 {
		return PaneDetails{}, ErrResponse
	}
	pane := (*panes.Result.Panes)[0]
	if pane.Workspace != workspace || !healthAtom(pane.Tab, true) {
		return PaneDetails{}, ErrResponse
	}
	return client.tabPane(ctx, tabLocation{Workspace: workspace, Tab: pane.Tab}, pane.ID)
}
