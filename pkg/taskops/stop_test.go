package taskops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/presentation"
	"github.com/roman/tincan/pkg/taskstate"
)

type stopRunner struct {
	socket     string
	exists     bool
	activeTab  string
	viewer     string
	workspace  string
	tab        string
	calls      int
	closes     int
	focusError bool
	onPaneRead func()
}

func (runner *stopRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls++
	if session != "work" || len(args) < 2 {
		return nil, nil, fmt.Errorf("unexpected session/arguments %s %v", session, args)
	}
	switch args[0] + " " + args[1] {
	case "session list":
		return []byte(
			fmt.Sprintf(
				`{"sessions":[{"name":"work","running":true,"socket_path":%q}]}`,
				runner.socket,
			),
		), nil, nil
	case "pane get":
		if runner.onPaneRead != nil {
			runner.onPaneRead()
		}
		if !runner.exists {
			return nil, []byte(`{"error":{"code":"pane_not_found"}}`), errors.New("missing")
		}
		if args[2] != "pane-1" {
			return nil, nil, errors.New("wrong pane")
		}
		return []byte(
			fmt.Sprintf(
				`{"result":{"pane":{"pane_id":"pane-1","workspace_id":%q,"tab_id":%q,"foreground_cwd":"/missing-worktree"}}}`,
				runner.workspace,
				runner.tab,
			),
		), nil, nil
	case "workspace list":
		return []byte(
			fmt.Sprintf(
				`{"result":{"workspaces":[{"workspace_id":"viewer-workspace","focused":true,"active_tab_id":%q}]}}`,
				runner.activeTab,
			),
		), nil, nil
	case "tab list":
		return []byte(
			fmt.Sprintf(`{"result":{"tabs":[{"tab_id":%q,"focused":true}]}}`, runner.activeTab),
		), nil, nil
	case "tab get":
		return []byte(
			`{"result":{"tab":{"tab_id":"viewer-tab","workspace_id":"viewer-workspace"}}}`,
		), nil, nil
	case "tab focus":
		if runner.focusError {
			return nil, nil, errors.New("focus restoration unavailable")
		}
		runner.activeTab = "viewer-tab"
		return []byte(`{"result":{}}`), nil, nil
	case "terminal title":
		return []byte(fmt.Sprintf(`{"result":{"reason":%q}}`, runner.viewer)), nil, nil
	case "pane close":
		if args[2] != "pane-1" {
			return nil, nil, errors.New("wrong pane closed")
		}
		runner.closes++
		runner.exists = false
		runner.activeTab = "other-tab"
		return []byte(`{"result":{}}`), nil, nil
	default:
		return nil, nil, fmt.Errorf("unexpected command %v", args)
	}
}

func stopFixture(t *testing.T) (*Service, *stopRunner) {
	t.Helper()
	service, _, _ := runningWorkerService(t)
	return service, &stopRunner{
		socket:    filepath.Join(t.TempDir(), "herdr.sock"),
		exists:    true,
		activeTab: "viewer-tab",
		viewer:    "cleared",
		workspace: "workspace-1",
		tab:       "tab-1",
	}
}

func TestStopClosesBoundPaneAndPreservesTaskRecords(t *testing.T) {
	for _, state := range []string{"queued", "in flight", "accepted close", "accepted retain"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			service, runner := stopFixture(t)
			if state == "queued" {
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.State = taskstate.Queued
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "accepted close" {
				if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "accepted retain" {
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					return task.PlaceHold(taskstate.Hold{Reason: "Waiting"})
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Retain, ""); err != nil {
					t.Fatal(err)
				}
			}
			record := filepath.Join(service.root, "records", "task-1.json")
			before, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := os.Stat(record)
			if err != nil {
				t.Fatal(err)
			}
			backlog := filepath.Join(service.root, "backlog.md")
			if err := os.Remove(backlog); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(backlog, 0o700); err != nil {
				t.Fatal(err)
			}
			client := herdr.Client{SessionRunner: runner}
			for range 2 {
				if err := service.Stop(ctx, "task-1", "gen-1", client); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			published, err := os.Stat(record)
			if err != nil || !bytes.Equal(before, after) || !os.SameFile(selected, published) ||
				runner.exists ||
				runner.closes != 1 ||
				runner.activeTab != "viewer-tab" {
				t.Fatalf(
					"Stop changed task or failed close: closes=%d focus=%s error=%v",
					runner.closes,
					runner.activeTab,
					err,
				)
			}
		})
	}
}

func TestStopRejectsWrongGenerationAndUnconfirmedWorkers(t *testing.T) {
	for _, state := range []string{"wrong generation", "empty generation", "no worker", "pending adoption", "pending generation", "pending launch", "missing location"} {
		t.Run(state, func(t *testing.T) {
			service, runner := stopFixture(t)
			generation := "gen-1"
			switch state {
			case "wrong generation":
				generation = "gen-other"
			case "empty generation":
				generation = ""
			default:
				if _, err := service.store.Update(context.Background(), "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					switch state {
					case "no worker":
						snapshot.State = taskstate.Queued
						snapshot.Worker = nil
					case "pending adoption":
						snapshot.State = taskstate.Queued
						snapshot.Worker.Workspace = ""
						snapshot.Worker.Tab = ""
						snapshot.Worker.PendingAdoption = true
					case "pending generation":
						snapshot.Worker.PendingGeneration = "gen-next"
					case "pending launch":
						snapshot.Worker.PendingLaunch = "gen-next"
					case "missing location":
						snapshot.Worker.Workspace = ""
						snapshot.Worker.Tab = ""
						snapshot.Completion = &taskstate.Completion{Generation: "gen-1", Mode: taskstate.Close, Phase: taskstate.Retired}
					}
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := service.Stop(context.Background(), "task-1", generation, herdr.Client{SessionRunner: runner}); err == nil {
				t.Fatal("invalid Stop accepted")
			}
			if runner.calls != 0 {
				t.Fatalf("invalid Stop contacted Herdr %d times", runner.calls)
			}
		})
	}
}

func TestStopHistoricalReceiptPreservesNewWorker(t *testing.T) {
	for _, position := range []string{"current", "prior"} {
		t.Run(position, func(t *testing.T) {
			service, runner := stopFixture(t)
			ctx := context.Background()
			before, err := service.store.Update(
				ctx,
				"task-1",
				func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.Receipt = &taskstate.Receipt{
						Generation: "gen-old",
						Mode:       taskstate.Retain,
					}
					if position == "prior" {
						snapshot.Receipt.Generation = "gen-recent"
						snapshot.PriorReceipts = []taskstate.Receipt{
							{Generation: "gen-old", Mode: taskstate.Retain},
						}
					}
					return taskstate.NewMigrated(snapshot)
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Stop(ctx, "task-1", "gen-old", herdr.Client{SessionRunner: runner}); err != nil {
				t.Fatal(err)
			}
			after, err := service.store.Read(ctx, "task-1")
			if err != nil || !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) ||
				runner.calls != 0 {
				t.Fatalf("receipt Stop = %#v, %v, calls %d", after.Snapshot(), err, runner.calls)
			}
		})
	}
}

func TestStopHoldsTaskLockDuringPaneClose(t *testing.T) {
	service, runner := stopFixture(t)
	runner.onPaneRead = func() {
		blocked, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := service.store.Update(blocked, "task-1", taskstate.Task.ReleaseHold); !errors.Is(
			err,
			context.DeadlineExceeded,
		) {
			t.Errorf("concurrent update = %v", err)
		}
	}
	if err := service.Stop(context.Background(), "task-1", "gen-1", herdr.Client{SessionRunner: runner}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.Update(context.Background(), "task-1", taskstate.Task.ReleaseHold); err != nil {
		t.Fatalf("lock not released: %v", err)
	}
}

func TestStopPreservesFocusFailureForRetry(t *testing.T) {
	service, runner := stopFixture(t)
	runner.focusError = true
	client := herdr.Client{SessionRunner: runner}
	if err := service.Stop(context.Background(), "task-1", "gen-1", client); err == nil ||
		runner.exists {
		t.Fatalf("focus failure = %v, pane exists = %v", err, runner.exists)
	}
	runner.focusError = false
	if err := service.Stop(context.Background(), "task-1", "gen-1", client); !errors.Is(
		err,
		presentation.ErrFocusUnsafe,
	) {
		t.Fatalf("live viewer recovery = %v", err)
	}
	runner.viewer = "no_foreground_client"
	if err := service.Stop(context.Background(), "task-1", "gen-1", client); err != nil ||
		runner.activeTab != "viewer-tab" ||
		runner.closes != 1 {
		t.Fatalf("focus retry = %v, tab=%s closes=%d", err, runner.activeTab, runner.closes)
	}
}

func TestStopRejectsStorageReplacementDuringPaneChecks(t *testing.T) {
	for _, change := range []string{"record replaced", "record edited in place", "records replaced", "root replaced"} {
		t.Run(change, func(t *testing.T) {
			service, runner := stopFixture(t)
			record := filepath.Join(service.root, "records", "task-1.json")
			before, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			current, err := service.store.Read(context.Background(), "task-1")
			if err != nil {
				t.Fatal(err)
			}
			edited, err := current.PlaceHold(taskstate.Hold{Reason: "External change"})
			if err != nil {
				t.Fatal(err)
			}
			changedData, err := edited.Encode()
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			runner.onPaneRead = func() {
				if changed {
					return
				}
				changed = true
				switch change {
				case "record replaced":
					path := filepath.Join(service.root, "new-record.json")
					if err := os.WriteFile(path, before, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(path, record); err != nil {
						t.Fatal(err)
					}
				case "record edited in place":
					if err := os.WriteFile(record, changedData, 0o600); err != nil {
						t.Fatal(err)
					}
				case "records replaced":
					path := filepath.Join(service.root, "records")
					if err := os.Rename(path, filepath.Join(service.root, "old-records")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				case "root replaced":
					if err := os.Rename(service.root, service.root+"-old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(service.root, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := service.Stop(context.Background(), "task-1", "gen-1", herdr.Client{SessionRunner: runner}); !errors.Is(
				err,
				taskstate.ErrInvalidTask,
			) {
				t.Fatalf("changed binding Stop = %v", err)
			}
			if runner.closes != 0 || !runner.exists {
				t.Fatalf("changed binding closed pane %d times", runner.closes)
			}
		})
	}
}

func TestStopRejectsTheViewedWorkerTab(t *testing.T) {
	service, runner := stopFixture(t)
	runner.workspace = "viewer-workspace"
	runner.tab = "viewer-tab"
	if _, err := service.store.Update(context.Background(), "task-1", func(task taskstate.Task) (taskstate.Task, error) {
		snapshot := task.Snapshot()
		snapshot.Worker.Workspace = runner.workspace
		snapshot.Worker.Tab = runner.tab
		return taskstate.NewMigrated(snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Stop(context.Background(), "task-1", "gen-1", herdr.Client{SessionRunner: runner}); !errors.Is(
		err,
		presentation.ErrFocusUnsafe,
	) {
		t.Fatalf("viewed worker tab Stop = %v", err)
	}
	if runner.closes != 0 || !runner.exists {
		t.Fatal("viewed worker tab was closed")
	}
}
