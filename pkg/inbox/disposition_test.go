package inbox

import (
	"errors"
	"sync"
	"testing"
)

func TestDispositionAllowsOneNewHandler(t *testing.T) {
	store := newTestStore(t)
	recipient := mustRecipient(t, "firstmate")
	message, err := store.Send(recipient, "reply-2", "Finished")
	if err != nil {
		t.Fatal(err)
	}
	const handlers = 12
	var group sync.WaitGroup
	group.Add(handlers)
	results := make(chan bool, handlers)
	errorsFound := make(chan error, handlers)
	for range handlers {
		go func() {
			defer group.Done()
			_, created, err := store.BeginDisposition(recipient, message.ID, "finish:task-1")
			if err != nil {
				errorsFound <- err
				return
			}
			results <- created
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	createdCount := 0
	for created := range results {
		if created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("new handlers=%d", createdCount)
	}
}

func TestDispositionSurvivesInterruptedHandling(t *testing.T) {
	store := newTestStore(t)
	recipient := mustRecipient(t, "firstmate")
	message, err := store.Send(recipient, "reply-1", "Finished")
	if err != nil {
		t.Fatal(err)
	}
	started, created, err := store.BeginDisposition(recipient, message.ID, "finish:task-1")
	if err != nil || !created || started.State != DispositionStarted {
		t.Fatalf("begin=%+v created=%t err=%v", started, created, err)
	}
	resumed, created, err := store.BeginDisposition(recipient, message.ID, "finish:task-1")
	if err != nil || created || resumed.State != DispositionStarted {
		t.Fatalf("resume=%+v created=%t err=%v", resumed, created, err)
	}
	completed, err := store.CompleteDisposition(recipient, message.ID, "done")
	if err != nil || completed.State != DispositionComplete || completed.Outcome != "done" {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}
	if err := store.Acknowledge(recipient, message.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.ReadDisposition(recipient, message.ID)
	if err != nil || persisted != completed {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	if _, _, err := reopened.BeginDisposition(recipient, message.ID, "finish:task-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.BeginDisposition(recipient, message.ID, "reopen:task-1"); !errors.Is(
		err,
		ErrDispositionConflict,
	) {
		t.Fatalf("changed action = %v", err)
	}
	if _, err := reopened.CompleteDisposition(recipient, message.ID, "queued"); !errors.Is(
		err,
		ErrDispositionConflict,
	) {
		t.Fatalf("changed outcome = %v", err)
	}
}
