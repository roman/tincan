package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// LaunchResources identifies possible effects of a recorded launch.
type LaunchResources struct{ Name, Label, Tab, Pane string }

// CheckLaunchAbsent checks every workspace, including launches recorded with a label.
func (client Client) CheckLaunchAbsent(ctx context.Context, launch LaunchResources) error {
	if !healthAtom(launch.Name, false) || !healthAtom(launch.Label, false) {
		return ErrResponse
	}
	if _, err := client.Get(ctx, launch.Name); !errors.Is(err, ErrNotFound) {
		if err != nil {
			return err
		}
		return ErrAgentPresent
	}
	output, err := client.run(ctx, "workspace", "list")
	if err != nil {
		return err
	}
	var response struct {
		Result struct {
			Workspaces *[]struct {
				ID string `json:"workspace_id"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if json.Unmarshal(output, &response) != nil || response.Result.Workspaces == nil {
		return fmt.Errorf("%w: invalid workspace inventory", ErrResponse)
	}
	seen := map[string]struct{}{}
	for _, workspace := range *response.Result.Workspaces {
		if !healthAtom(workspace.ID, false) {
			return ErrResponse
		}
		if _, exists := seen[workspace.ID]; exists {
			return ErrResponse
		}
		seen[workspace.ID] = struct{}{}
		if err := client.checkLaunchWorkspaceAbsent(ctx, workspace.ID, launch); err != nil {
			return err
		}
	}
	return nil
}

func (client Client) checkLaunchWorkspaceAbsent(
	ctx context.Context,
	workspace string,
	launch LaunchResources,
) error {
	output, err := client.run(ctx, "tab", "list", "--workspace", workspace)
	if err != nil {
		return err
	}
	var tabs struct {
		Result struct {
			Tabs *[]tabLocation `json:"tabs"`
		} `json:"result"`
	}
	if json.Unmarshal(output, &tabs) != nil || tabs.Result.Tabs == nil {
		return fmt.Errorf("%w: invalid tab inventory", ErrResponse)
	}
	for _, tab := range *tabs.Result.Tabs {
		if tab.Workspace != workspace || !healthAtom(tab.Tab, true) {
			return ErrResponse
		}
		if tab.Label == launch.Label || (launch.Tab != "" && tab.Tab == launch.Tab) {
			return errors.New("launch tab still exists; inspect it before abandonment")
		}
	}
	if launch.Pane == "" {
		return nil
	}
	output, err = client.run(ctx, "pane", "list", "--workspace", workspace)
	if err != nil {
		return err
	}
	var panes struct {
		Result struct {
			Panes *[]struct {
				ID        string `json:"pane_id"`
				Workspace string `json:"workspace_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	if json.Unmarshal(output, &panes) != nil || panes.Result.Panes == nil {
		return fmt.Errorf("%w: invalid pane inventory", ErrResponse)
	}
	for _, pane := range *panes.Result.Panes {
		if pane.Workspace != workspace || !healthAtom(pane.ID, true) {
			return ErrResponse
		}
		if pane.ID == launch.Pane {
			return errors.New("launch pane still exists; inspect it before abandonment")
		}
	}
	return nil
}
