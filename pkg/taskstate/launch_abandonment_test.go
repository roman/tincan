package taskstate

import (
	"errors"
	"testing"
)

func TestAbandonmentCannotShareActiveOrCompletedGeneration(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*Snapshot)
	}{
		{"worker", func(s *Snapshot) {
			worker := workerForTransition()
			worker.Workspace, worker.Tab = "w1", "t1"
			worker.Generation = "abandoned"
			s.Worker = &worker
		}},
		{"pending generation", func(s *Snapshot) {
			worker := workerForTransition()
			worker.Workspace, worker.Tab = "w1", "t1"
			worker.PendingGeneration = "abandoned"
			s.Worker = &worker
		}},
		{"pending launch", func(s *Snapshot) {
			worker := workerForTransition()
			worker.Workspace, worker.Tab = "w1", "t1"
			worker.PendingLaunch = "abandoned"
			s.Worker = &worker
		}},
		{"launch intent", func(s *Snapshot) {
			intent := launchForTest()
			intent.Generation = "abandoned"
			intent.Phase = LaunchReserved
			s.LaunchIntent = &intent
		}},
		{"completion", func(s *Snapshot) { s.Completion = &Completion{Generation: "abandoned", Mode: Retain, Phase: Retired} }},
		{"receipt", func(s *Snapshot) { s.Receipt = &Receipt{Generation: "abandoned", Mode: Retain} }},
		{"prior receipt", func(s *Snapshot) {
			s.Receipt = &Receipt{Generation: "latest", Mode: Retain}
			s.PriorReceipts = []Receipt{{Generation: "abandoned", Mode: Retain}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := taskForTransition(t).Snapshot()
			test.configure(&snapshot)
			if _, err := NewMigrated(snapshot); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			snapshot.AbandonedLaunches = []LaunchAbandonment{
				{Generation: "abandoned", Worktree: "/worktree", Branch: "task/work"},
			}
			if _, err := NewMigrated(snapshot); !errors.Is(err, ErrInvalidTask) {
				t.Fatalf("accepted ambiguous generation: %v", err)
			}
		})
	}
}
