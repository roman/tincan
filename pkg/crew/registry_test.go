package crew

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func testRecord() Record {
	return Record{
		TaskID: "task-1", Name: "worker-1", Kind: "codex", PaneID: "pane-1",
		HerdrSession: "work",
		Project:      "/project", Worktree: "/project-worktree", SpawnGeneration: "gen-1",
		AgentArgs: []string{"resume", "thread-1"},
	}
}

func TestRegistryPersistsCrewAcrossInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord()
	if err := registry.Register(record); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get(record.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("got %+v, want %+v", got, record)
	}
	listed, err := reopened.List()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listed, []Record{record}) {
		t.Fatalf("got %+v, want one registered worker", listed)
	}
}

func TestRegisterAfterReservesWorkerBeforePreparation(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord()
	interrupted := errors.New("interrupted preparation")
	if err := registry.registerAfter(record, func() error { return interrupted }); !errors.Is(
		err,
		interrupted,
	) {
		t.Fatalf("interrupted adoption: %v", err)
	}
	pending, err := registry.Get(record.TaskID)
	if err != nil || !pending.PendingAdoption {
		t.Fatalf("pending record=%+v err=%v", pending, err)
	}
	rival := testRecord()
	rival.TaskID = "task-2"
	if err := registry.Register(rival); !errors.Is(err, ErrConflict) {
		t.Fatalf("rival claimed reserved worker: %v", err)
	}
	if err := registry.registerAfter(record, func() error { return nil }); err != nil {
		t.Fatalf("retry: %v", err)
	}
	completed, err := registry.Get(record.TaskID)
	if err != nil || !reflect.DeepEqual(completed, record) {
		t.Fatalf("completed record=%+v err=%v", completed, err)
	}
}

func TestRegistryRejectsConflictingRegistration(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord()
	if err := registry.Register(record); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(record); err != nil {
		t.Fatalf("same registration must be idempotent: %v", err)
	}
	changed := record
	changed.PaneID = "pane-2"
	if err := registry.Register(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want conflict", err)
	}
	other := record
	other.TaskID = "task-2"
	if err := registry.Register(other); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate worker identity: got %v, want conflict", err)
	}
	other.Name = "worker-2"
	if err := registry.Register(other); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate pane: got %v, want conflict", err)
	}
}

func TestRegistryListsTasksInIDOrder(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second := testRecord()
	second.TaskID, second.Name, second.PaneID = "task-2", "worker-2", "pane-2"
	first := testRecord()
	if err := registry.Register(second); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}
	listed, err := registry.List()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listed, []Record{first, second}) {
		t.Fatalf("got %+v, want task ID order", listed)
	}
}

func TestRegistryRejectsOversizedLaunchRecord(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord()
	record.AgentArgs = []string{strings.Repeat("x", 1<<20)}
	if err := registry.Register(record); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("got %v, want invalid record", err)
	}
}

func TestRegistryRejectsMissingArgumentsAndInvalidSession(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord()
	record.AgentArgs = nil
	if err := registry.Register(record); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("missing arguments: got %v", err)
	}
	record = testRecord()
	record.HerdrSession = "bad:session"
	if err := registry.Register(record); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid session: got %v", err)
	}
}

func TestRegistryAllowsSameTargetInDifferentSessions(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := testRecord()
	second := first
	second.TaskID = "task-2"
	second.HerdrSession = "other"
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(second); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrySerializesConflictingRegistrations(t *testing.T) {
	root := t.TempDir()
	first := testRecord()
	second := first
	second.TaskID = "task-2"
	var wait sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, record := range []Record{first, second} {
		wait.Add(1)
		go func(record Record) {
			defer wait.Done()
			registry, err := New(root)
			if err == nil {
				<-start
				err = registry.Register(record)
			}
			results <- err
		}(record)
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected registration error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes %d, conflicts %d", successes, conflicts)
	}
	registry, _ := New(root)
	listed, err := registry.List()
	if err != nil || len(listed) != 1 {
		t.Fatalf("records %+v, error %v", listed, err)
	}
}

func TestRegistryRejectsSymlinkedDirectories(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(parent, "linked")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	registry, _ := New(linkedRoot)
	if err := registry.Register(testRecord()); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("linked root: got %v", err)
	}
	if err := os.Symlink(realRoot, filepath.Join(realRoot, "crew")); err != nil {
		t.Fatal(err)
	}
	registry, _ = New(realRoot)
	if err := registry.Register(testRecord()); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("linked crew directory: got %v", err)
	}
}

func TestRegistryLockTimesOut(t *testing.T) {
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(root, "crew", ".lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	second := testRecord()
	second.TaskID, second.Name, second.PaneID = "task-2", "worker-2", "pane-2"
	if err := registry.Register(second); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("got %v, want lock timeout", err)
	}
}

func TestRegistryRejectsInvalidIdentityAndSymlinkRecord(t *testing.T) {
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord()
	record.TaskID = "../escape"
	if err := registry.Register(record); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("got %v, want invalid record", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "crew"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "crew", "task-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRecord()); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("got %v, want invalid symlink record", err)
	}
}
