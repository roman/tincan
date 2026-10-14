package taskops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

type ownedLaunchRunner struct {
	*initialLaunchRunner
	workspaceCreates int
	loseResponse     bool
}

func (runner *ownedLaunchRunner) RunSession(
	ctx context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	switch strings.Join(args[:2], " ") {
	case "workspace create":
		current, err := runner.service.store.Read(ctx, "task-1")
		if err != nil || current.Snapshot().LaunchIntent.Phase != taskstate.LaunchPaneRequested {
			return nil, nil, errors.New("workspace creation precedes reservation")
		}
		for index, arg := range args {
			if arg == "--cwd" && index+1 < len(args) {
				runner.intent.Worktree = args[index+1]
			}
		}
		runner.workspaceCreates++
		runner.created = true
		if runner.loseResponse {
			return nil, nil, errors.New("workspace response lost")
		}
		return []byte(
			fmt.Sprintf(
				`{"result":{"workspace":{"workspace_id":"w1","label":%q},"tab":{"workspace_id":"w1","tab_id":"w1:t2"},"root_pane":{"pane_id":"w1:p2"}}}`,
				runner.intent.Label,
			),
		), nil, nil
	case "workspace list":
		return []byte(
			fmt.Sprintf(
				`{"result":{"workspaces":[{"workspace_id":"w1","label":%q}]}}`,
				runner.intent.Label,
			),
		), nil, nil
	default:
		return runner.initialLaunchRunner.RunSession(ctx, session, args...)
	}
}

func TestInitialLaunchOwnsWorkspaceAndRecoversLostResponse(t *testing.T) {
	for _, loseResponse := range []bool{false, true} {
		t.Run(fmt.Sprint(loseResponse), func(t *testing.T) {
			service, base, config := initialLaunchFixture(t)
			ctx := context.Background()
			if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.CancelInitialLaunch("gen-1") }); err != nil {
				t.Fatal(err)
			}
			base.intent.Workspace = ""
			if _, err := service.store.ReserveInitialLaunch(ctx, "task-1", base.intent); err != nil {
				t.Fatalf("owned workspace reservation: %v", err)
			}
			runner := &ownedLaunchRunner{initialLaunchRunner: base, loseResponse: loseResponse}
			config.Herdr.SessionRunner = runner
			result, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
			if loseResponse {
				if err == nil {
					t.Fatal("lost response unexpectedly succeeded")
				}
				result, err = service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
			}
			if err != nil {
				t.Fatal(err)
			}
			worker := result.Snapshot().Worker
			encoded, _ := json.Marshal(worker)
			if worker == nil || worker.Workspace != "w1" ||
				!strings.Contains(string(encoded), `"owned_workspace_label":"concierge-gen-1"`) ||
				runner.workspaceCreates != 1 ||
				runner.creates != 0 {
				t.Fatalf(
					"owned launch: worker=%s workspace creates=%d tab creates=%d",
					encoded,
					runner.workspaceCreates,
					runner.creates,
				)
			}
		})
	}
}
