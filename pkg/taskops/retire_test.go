package taskops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/taskstate"
)

type retirementRunner struct {
	pane     string
	response string
	calls    int
	onCheck  func()
}

func (runner *retirementRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls++
	pane := runner.pane
	if pane == "" {
		pane = "pane-1"
	}
	if session != "work" ||
		!reflect.DeepEqual(args, []string{"pane", "get", pane, "--session", "work"}) {
		return nil, nil, fmt.Errorf("unexpected pane check: %s %v", session, args)
	}
	if runner.onCheck != nil {
		runner.onCheck()
	}
	return []byte(runner.response), nil, nil
}

func TestRetireClearsCompletedWorkerAndRepublishesReceipt(t *testing.T) {
	for _, mode := range []taskstate.CompletionMode{taskstate.Close, taskstate.Retain} {
		t.Run(string(mode), func(t *testing.T) {
			service, _, _ := runningWorkerService(t)
			ctx := context.Background()
			closedOn := "2026-10-12"
			wantState := taskstate.Done
			if mode == taskstate.Retain {
				closedOn = ""
				wantState = taskstate.Queued
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					return task.PlaceHold(taskstate.Hold{Reason: "Waiting"})
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := service.Finish(ctx, "task-1", "gen-1", mode, closedOn); err != nil {
				t.Fatal(err)
			}
			runner := &retirementRunner{response: `{"error":{"code":"pane_not_found"}}`}
			client := herdr.Client{SessionRunner: runner}
			retired, err := service.Retire(ctx, "task-1", "gen-1", client)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := retired.Snapshot()
			if snapshot.State != wantState || snapshot.Worker != nil ||
				snapshot.Completion != nil ||
				!retired.HasReceipt("gen-1", mode) {
				t.Fatalf("retired task = %#v", snapshot)
			}
			path := filepath.Join(service.root, "records", "task-1.json")
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Retire(ctx, "task-1", "gen-1", client); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if runner.calls != 1 || os.SameFile(before, after) {
				t.Fatalf(
					"receipt retry calls=%d, republished=%v",
					runner.calls,
					!os.SameFile(before, after),
				)
			}
		})
	}
}

func TestRetireRejectsUnconfirmedPaneAndUnmatchedGeneration(t *testing.T) {
	for _, test := range []struct{ name, response, generation string }{
		{"present", `{"result":{"pane":{"pane_id":"pane-1"}}}`, "gen-1"},
		{"missing agent", `{"error":{"code":"agent_not_found"}}`, "gen-1"},
		{"unreadable", `not-json`, "gen-1"},
		{"wrong generation", `{"error":{"code":"pane_not_found"}}`, "gen-other"},
		{"missing generation", `{"error":{"code":"pane_not_found"}}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := runningWorkerService(t)
			ctx := context.Background()
			if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
				t.Fatal(err)
			}
			before, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			runner := &retirementRunner{response: test.response}
			if _, err := service.Retire(ctx, "task-1", test.generation, herdr.Client{SessionRunner: runner}); err == nil {
				t.Fatal("invalid retirement accepted")
			}
			after, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatal("failed retirement changed task")
			}
			if test.generation != "gen-1" && runner.calls != 0 {
				t.Fatal("unmatched generation contacted Herdr")
			}
		})
	}
}

func TestRetireHistoricalReceiptPreservesNewCompletion(t *testing.T) {
	for _, position := range []string{"current", "prior"} {
		t.Run(position, func(t *testing.T) {
			service, _, _ := runningWorkerService(t)
			ctx := context.Background()
			if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
				snapshot := task.Snapshot()
				receipt := taskstate.Receipt{Generation: "gen-old", Mode: taskstate.Retain}
				if position == "current" {
					snapshot.Receipt = &receipt
				} else {
					snapshot.Receipt = &taskstate.Receipt{Generation: "gen-recent", Mode: taskstate.Retain}
					snapshot.PriorReceipts = []taskstate.Receipt{receipt}
				}
				return taskstate.NewMigrated(snapshot)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
				t.Fatal(err)
			}
			before, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			recordPath := filepath.Join(service.root, "records", "task-1.json")
			selected, err := os.Stat(recordPath)
			if err != nil {
				t.Fatal(err)
			}
			retired, err := service.Retire(ctx, "task-1", "gen-old", herdr.Client{})
			if err != nil || !reflect.DeepEqual(before.Snapshot(), retired.Snapshot()) {
				t.Fatalf("historical retry = %#v, %v", retired.Snapshot(), err)
			}
			published, err := os.Stat(recordPath)
			if err != nil || os.SameFile(selected, published) {
				t.Fatalf("historical receipt was not republished: %v", err)
			}
		})
	}
}

func TestRetireMigratedWorkerRequiresExactPaneDeparture(t *testing.T) {
	for _, response := range []string{`{"result":{"pane":{"pane_id":"pane-1"}}}`, `not-json`} {
		t.Run(response, func(t *testing.T) {
			service, _, _ := runningWorkerService(t)
			ctx := context.Background()
			if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
				t.Fatal(err)
			}
			before, err := service.store.Update(
				ctx,
				"task-1",
				func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.Completion.Phase = taskstate.Retired
					return taskstate.NewMigrated(snapshot)
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			runner := &retirementRunner{response: response}
			if _, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{SessionRunner: runner}); err == nil {
				t.Fatal("migrated worker retired without exact pane departure")
			}
			after, err := service.store.Read(ctx, "task-1")
			if err != nil || runner.calls != 1 ||
				!reflect.DeepEqual(before.Snapshot(), after.Snapshot()) {
				t.Fatalf(
					"migrated retirement = %#v, %v; calls=%d",
					after.Snapshot(),
					err,
					runner.calls,
				)
			}
		})
	}
}

func TestRetireMigratedCompletionBeforeAndAfterQueueTransition(t *testing.T) {
	for _, receipt := range []string{"absent", "matching"} {
		t.Run(receipt, func(t *testing.T) {
			service, _, _ := runningWorkerService(t)
			ctx := context.Background()
			if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
				snapshot := task.Snapshot()
				snapshot.Worker = nil
				snapshot.Completion = &taskstate.Completion{Generation: "gen-1", Mode: taskstate.Close, Phase: taskstate.Retired}
				return taskstate.NewMigrated(snapshot)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{}); !errors.Is(
				err,
				taskstate.ErrInvalidTransition,
			) {
				t.Fatalf("retire before finish = %v", err)
			}
			if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
				t.Fatal(err)
			}
			if receipt == "matching" {
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.Receipt = &taskstate.Receipt{Generation: "gen-1", Mode: taskstate.Close}
					return taskstate.NewMigrated(snapshot)
				}); err != nil {
					t.Fatal(err)
				}
			}
			retired, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{})
			if err != nil || retired.Snapshot().Completion != nil ||
				!retired.HasReceipt("gen-1", taskstate.Close) {
				t.Fatalf("migrated cleanup = %#v, %v", retired.Snapshot(), err)
			}
		})
	}
}

func TestRetireHoldsTaskLockDuringPaneCheckAndRepairsProjection(t *testing.T) {
	service, _, _ := runningWorkerService(t)
	ctx := context.Background()
	if _, err := service.Finish(ctx, "task-1", "gen-1", taskstate.Close, "2026-10-12"); err != nil {
		t.Fatal(err)
	}
	runner := &retirementRunner{response: `{"error":{"code":"pane_not_found"}}`}
	runner.onCheck = func() {
		blocked, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()
		if _, err := service.store.Update(blocked, "task-1", taskstate.Task.ReleaseHold); !errors.Is(
			err,
			context.DeadlineExceeded,
		) {
			t.Errorf("concurrent update = %v", err)
		}
	}
	backlog := filepath.Join(service.root, "backlog.md")
	if err := os.Remove(backlog); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backlog, 0o700); err != nil {
		t.Fatal(err)
	}
	retired, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{SessionRunner: runner})
	if err == nil || !retired.HasReceipt("gen-1", taskstate.Close) {
		t.Fatalf("projection failure = %#v, %v", retired.Snapshot(), err)
	}
	if err := os.Remove(backlog); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Retire(ctx, "task-1", "gen-1", herdr.Client{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backlog); err != nil {
		t.Fatal(err)
	}
}
