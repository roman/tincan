package hostservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

// NativeCheckpoint reports an observed blocker without deciding its native policy.
type NativeCheckpoint struct {
	Kind         string `json:"kind"`
	Notification string `json:"notification"`
}

// NativeCheckpointHelp requires an operator decision in the existing native dialog.
func NativeCheckpointHelp(intent *taskstate.LaunchIntent) string {
	return fmt.Sprintf(
		"Review the native dialog in session %q, pane %q. Ask the operator before accepting folder trust or changing permissions. Inspect with concierge control --session %q status %q. After the dialog is resolved, retry the exact original launch; automatic reconciliation remains active.",
		intent.Session,
		intent.Endpoint.Pane,
		intent.Session,
		intent.Endpoint.Pane,
	)
}

func (server *Server) launchNativeCheckpoint(
	ctx context.Context,
	taskID string,
) (*NativeCheckpoint, error) {
	inspection, cancel := context.WithTimeout(context.WithoutCancel(ctx), launchInspectionTimeout)
	defer cancel()
	task, err := server.store.Read(inspection, taskID)
	if err != nil {
		return nil, nil
	}
	intent := task.Snapshot().LaunchIntent
	if intent == nil || intent.Endpoint == nil || intent.Phase != taskstate.LaunchAgentRequested {
		return nil, nil
	}
	client, err := herdr.NewClient(server.config.HerdrExecutable).ForSession(intent.Session)
	if err != nil {
		return nil, nil
	}
	details, err := client.PaneDetails(inspection, intent.Endpoint.Pane)
	if err != nil || details.WorkspaceID != intent.Endpoint.Workspace ||
		details.TabID != intent.Endpoint.Tab {
		return nil, nil
	}
	recorded, err := os.Stat(intent.Worktree)
	if err != nil {
		return nil, nil
	}
	live, err := os.Stat(details.ForegroundDirectory)
	if err != nil || !recorded.IsDir() || !live.IsDir() || !os.SameFile(recorded, live) {
		return nil, nil
	}
	agent, err := client.Get(inspection, intent.Name)
	if err != nil || !blockedLaunchAgent(agent, intent) {
		return nil, nil
	}
	kind := "native_input"
	if screen, err := client.VisibleScreen(inspection, intent.Endpoint.Pane); err == nil &&
		intent.Harness == "claude" {
		text := strings.ToLower(strings.Join(strings.Fields(screen), " "))
		if strings.Contains(text, "accessing workspace:") &&
			strings.Contains(text, "yes, i trust this folder") &&
			strings.Contains(text, "no, exit") &&
			strings.Contains(text, "enter to confirm") {
			kind = "folder_trust"
		}
	}
	// A changed agent or a completed dialog invalidates the captured screen.
	agent, err = client.Get(inspection, intent.Name)
	if err != nil || !blockedLaunchAgent(agent, intent) {
		return nil, nil
	}
	checkpoint := &NativeCheckpoint{Kind: kind, Notification: "failed"}
	body, err := json.Marshal(struct {
		Kind       string `json:"kind"`
		State      string `json:"state"`
		Task       string `json:"task"`
		Generation string `json:"generation"`
		Checkpoint string `json:"checkpoint"`
		Harness    string `json:"harness"`
		Session    string `json:"session"`
		Pane       string `json:"pane"`
		Worktree   string `json:"worktree"`
		Help       string `json:"help"`
	}{"task-native-checkpoint", filepath.Join(server.config.Root, "tasks"), taskID, intent.Generation, kind, intent.Harness, intent.Session, intent.Endpoint.Pane, intent.Worktree, NativeCheckpointHelp(intent)})
	if err != nil {
		return checkpoint, err
	}
	messages, err := inbox.New(server.config.MessageRoot)
	if err != nil {
		return checkpoint, err
	}
	key := fmt.Sprintf(
		"task-native-checkpoint:%s:%s:%s:%s",
		server.config.Root,
		taskID,
		intent.Generation,
		kind,
	)
	if _, err := messages.Send(server.config.Recipient, key, string(body)); err != nil {
		return checkpoint, fmt.Errorf("native checkpoint notification is unconfirmed: %w", err)
	}
	checkpoint.Notification = "sent"
	return checkpoint, nil
}

func blockedLaunchAgent(agent herdr.Agent, intent *taskstate.LaunchIntent) bool {
	return agent.PaneID == intent.Endpoint.Pane && agent.Kind == intent.Harness &&
		agent.Status == herdr.Blocked
}

func validateNativeCheckpoint(response Response) error {
	checkpoint := response.Checkpoint
	if checkpoint == nil {
		return nil
	}
	intent := response.Transition.LaunchIntent
	if response.Outcome != Blocked || intent == nil || intent.Endpoint == nil ||
		intent.Phase != taskstate.LaunchAgentRequested ||
		(checkpoint.Kind != "native_input" && checkpoint.Kind != "folder_trust") ||
		(checkpoint.Kind == "folder_trust" && intent.Harness != "claude") ||
		(checkpoint.Notification != "sent" && checkpoint.Notification != "failed") {
		return errors.New("host returned an invalid native checkpoint")
	}
	return nil
}
