package resident

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/supervise"
)

type observedWorker struct{ checks atomic.Int32 }

func (worker *observedWorker) Observe(context.Context) error {
	worker.checks.Add(1)
	return nil
}

func (*observedWorker) Restart(context.Context) error { return nil }

func TestControllerSupervisesAndWakesTask(t *testing.T) {
	store, err := inbox.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	bell := recordingDoorbell{calls: make(chan string, 2)}
	worker := &observedWorker{}
	controller := Controller{
		Watcher:     Watcher{Store: store, Recipient: recipient, Target: "pane-1", Doorbell: bell},
		Worker:      worker,
		Supervision: supervise.Config{Interval: 20 * time.Millisecond, RestartTolerance: 5, RestartWindow: time.Second},
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	ended := make(chan error, 1)
	go func() { ended <- controller.Run(ctx, ready) }()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not become ready")
	}
	if _, err := store.Send(recipient, "work", "Read task"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bell.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not wake worker")
	}
	cancel()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not stop")
	}
	if worker.checks.Load() == 0 {
		t.Fatal("worker was not supervised")
	}
}
