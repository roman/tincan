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
