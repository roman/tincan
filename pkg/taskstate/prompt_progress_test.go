package taskstate

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/herdr"
)

func TestPromptBudgetRequiresMatchingAssignmentAndSurvivesSnapshots(t *testing.T) {
	id := "k-" + strings.Repeat("a", 64)
	other := "k-" + strings.Repeat("b", 64)
	started := time.Now().UTC()
	task, err := runningTaskForRestart(t).BeginPromptProgress(id, started)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []func() (Task, error){
		func() (Task, error) { return task.BeginPromptProgress(other, started) },
		func() (Task, error) { return task.BeginPromptProgress(id, started.Add(time.Second)) },
		func() (Task, error) { return task.ReservePromptAttempt(other, started) },
		func() (Task, error) { return task.ReservePromptAttempt(id, started.Add(-time.Second)) },
		func() (Task, error) { return task.ClearPromptProgress(other) },
		func() (Task, error) {
			return task.FreezeStallAlert(id, StallAlert{ObservedAt: started.Add(-time.Second), State: herdr.Done})
		},
	} {
		if _, err := attempt(); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("invalid transition = %v", err)
		}
	}
	for range MaximumPromptAttempts {
		task, err = task.ReservePromptAttempt(id, started)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := task.ReservePromptAttempt(id, started); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unbounded attempts = %v", err)
	}
	alert := StallAlert{
		ObservedAt: started.Add(5 * time.Minute),
		State:      herdr.Done,
		Screen:     "Credits exhausted",
	}
	task, err = task.FreezeStallAlert(id, alert)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := task.Snapshot()
	snapshot.Worker.PromptProgress.Alert.Screen = "mutated"
	if task.Snapshot().Worker.PromptProgress.Alert.Screen != alert.Screen {
		t.Fatal("snapshot changed frozen alert")
	}
	changed := alert
	changed.Screen = "different"
	if _, err := task.FreezeStallAlert(id, changed); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("alert replacement = %v", err)
	}
	reserved, err := task.ReserveLaunch("gen-2")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := reserved.AdvanceGeneration("gen-2")
	if err != nil || restarted.Snapshot().Worker.PromptProgress != nil {
		t.Fatalf("restart retained old budget: %+v, %v", restarted.Snapshot(), err)
	}
	cleared, err := task.ClearPromptProgress(id)
	if err != nil || cleared.Snapshot().Worker.PromptProgress != nil {
		t.Fatalf("clear = %+v, %v", cleared.Snapshot(), err)
	}
}

func TestPromptAlertRejectsEvidenceBeforeLastAttempt(t *testing.T) {
	id := "k-" + strings.Repeat("a", 64)
	started := time.Now().UTC()
	task, err := runningTaskForRestart(t).BeginPromptProgress(id, started)
	if err != nil {
		t.Fatal(err)
	}
	task, err = task.ReservePromptAttempt(id, started.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.FreezeStallAlert(id, StallAlert{ObservedAt: started, State: herdr.Done}); err == nil {
		t.Fatal("accepted an alert before its prompt attempt")
	}
}
