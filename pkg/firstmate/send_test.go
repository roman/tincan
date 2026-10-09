package firstmate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/herdr"
)

type testDoorbell struct {
	mu     sync.Mutex
	calls  int
	target herdr.Target
	text   string
	result herdr.RingResult
}

func (doorbell *testDoorbell) Ring(
	_ context.Context,
	target herdr.Target,
	text string,
) herdr.RingResult {
	doorbell.mu.Lock()
	defer doorbell.mu.Unlock()
	doorbell.calls++
	doorbell.target, doorbell.text = target, text
	return doorbell.result
}
func newTestSender(t *testing.T) (Sender, string, *testDoorbell) {
	t.Helper()
	state, id, _ := testLockInputs(t)
	if err := os.WriteFile(filepath.Join(state.String(), id.String()+".meta"), []byte(validMetadata), 0600); err != nil {
		t.Fatal(err)
	}
	actor, _ := ParseLeaseActor("main")
	prompt := &testDoorbell{}
	return Sender{
			StateDir: state,
			TaskID:   id,
			Actor:    actor,
			Doorbell: prompt,
			Clock:    func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600)) },
		}, filepath.Join(
			state.String(),
			id.String()+".inbox",
		), prompt
}
func TestSenderExactRecordAndSequence(t *testing.T) {
	sender, inbox, prompt := newTestSender(t)
	body := "  first\nsecond\n"
	first, err := sender.Send(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first.RecordPath.String()) != "001.msg" {
		t.Fatalf("first record = %q", first.RecordPath.String())
	}
	want := "schema=fm-task-inbox.v1\nat=2026-01-02T02:04:05Z\n--\n" + body
	data, err := os.ReadFile(first.RecordPath.String())
	if err != nil || string(data) != want {
		t.Fatalf("record = %q, %v", data, err)
	}
	if info, err := os.Stat(first.RecordPath.String()); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("record mode = %v, %v", info, err)
	}
	if prompt.target.Session() != "crew" || prompt.target.PaneID() != "pane-1" ||
		prompt.text != `: Firstmate instruction waiting: list "$FM_TASK_INBOX"/*.msg in your 'task-1.inbox' steering inbox, read and act on each in numeric order, then mv each into its handled/.` {
		t.Fatalf("doorbell = %+v %q", prompt.target, prompt.text)
	}
	if err := os.Rename(first.RecordPath.String(), filepath.Join(inbox, "handled", "001.msg")); err != nil {
		t.Fatal(err)
	}
	second, err := sender.Send(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(second.RecordPath.String()) != "002.msg" {
		t.Fatalf("second record = %q", second.RecordPath.String())
	}
	third, err := sender.Send(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(third.RecordPath.String()) != "003.msg" {
		t.Fatalf("third record = %q", third.RecordPath.String())
	}
}
func TestSenderBlankBodyHasNoEffects(t *testing.T) {
	for _, body := range []string{"", " \t\n"} {
		t.Run(strings.ReplaceAll(body, "\n", "newline"), func(t *testing.T) {
			sender, _, _ := newTestSender(t)
			if _, err := sender.Send(context.Background(), body); !errors.Is(err, ErrBlankMessage) {
				t.Fatalf("blank send = %v", err)
			}
			entries, err := os.ReadDir(sender.StateDir.String())
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "task-1.meta" {
				t.Fatalf("blank send effects = %v", entries)
			}
		})
	}
}
func TestSenderDoorbellFailureIsRecordedSuccess(t *testing.T) {
	sender, _, prompt := newTestSender(t)
	prompt.result = herdr.RingResult{
		Outcome: herdr.RingFailed,
		Err:     errors.New("terminal unavailable"),
	}
	result, err := sender.Send(context.Background(), "hello")
	if err != nil || result.Doorbell.Outcome != herdr.RingFailed ||
		!errors.Is(result.Doorbell.Err, prompt.result.Err) {
		t.Fatalf("send = %+v, %v", result, err)
	}
	if _, err := os.Stat(result.RecordPath.String()); err != nil {
		t.Fatal(err)
	}
}
func TestSenderOppositeLeaseRefusesWithoutRecord(t *testing.T) {
	sender, inbox, _ := newTestSender(t)
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(sender.StateDir.String(), ".lock"), []byte(pid+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sender.StateDir.String(), ".lease-task-1"), []byte("branch\t"+pid+"\t1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Send(context.Background(), "hello"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("lease send = %v", err)
	}
	if _, err := os.Stat(inbox); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inbox = %v", err)
	}
}
func TestSenderConcurrentSequence(t *testing.T) {
	sender, inbox, _ := newTestSender(t)
	const workers = 8
	var group sync.WaitGroup
	results := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := sender.writeRecord(context.Background(), "same body")
			results <- err
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".msg") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if strings.Join(
		names,
		",",
	) != "001.msg,002.msg,003.msg,004.msg,005.msg,006.msg,007.msg,008.msg" {
		t.Fatalf("records = %v", names)
	}
}

func TestSenderRejectsChangedMetadataWhileWaiting(t *testing.T) {
	for _, test := range []struct{ name, changed string }{
		{"endpoint", strings.Replace(validMetadata, "window=crew:pane-1", "window=crew:pane-2", 1)},
		{"generation", strings.Replace(validMetadata, "spawn_gen=s123.456.7", "spawn_gen=s123.456.8", 1)},
		{"backend", strings.Replace(validMetadata, "backend=herdr", "backend=other", 1)},
		{"remote", validMetadata + "remote_host=example\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender, inbox, _ := newTestSender(t)
			lock, err := AcquireTaskLock(sender.StateDir, sender.TaskID, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			results := make(chan error, 1)
			go func() { _, err := sender.Send(context.Background(), "hello"); results <- err }()
			guardPath := filepath.Join(sender.StateDir.String(), ".fm-lease-command.lock")
			deadline := time.Now().Add(time.Second)
			for {
				if _, err := os.Lstat(guardPath); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("sender did not enter lease guard")
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(150 * time.Millisecond)
			if err := os.WriteFile(filepath.Join(sender.StateDir.String(), "task-1.meta"), []byte(test.changed), 0600); err != nil {
				t.Fatal(err)
			}
			if err := lock.Release(); err != nil {
				t.Fatal(err)
			}
			if err := <-results; !errors.Is(err, ErrTaskChanged) {
				t.Fatalf("changed metadata send = %v", err)
			}
			if _, err := os.Stat(inbox); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inbox = %v", err)
			}
		})
	}
}

func TestSenderMetadataLockTimeoutLeavesNoRecord(t *testing.T) {
	sender, inbox, _ := newTestSender(t)
	lock, err := AcquireTaskLock(sender.StateDir, sender.TaskID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := sender.Send(ctx, "hello"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed send = %v", err)
	}
	if _, err := os.Stat(inbox); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inbox = %v", err)
	}
}

type doorbellFunc func(context.Context, herdr.Target, string) herdr.RingResult

func (doorbell doorbellFunc) Ring(
	ctx context.Context,
	target herdr.Target,
	text string,
) herdr.RingResult {
	return doorbell(ctx, target, text)
}

func TestSenderReleasesMetadataBeforeDoorbellAndRetainsLease(t *testing.T) {
	sender, _, _ := newTestSender(t)
	sender.Doorbell = doorbellFunc(
		func(_ context.Context, _ herdr.Target, _ string) herdr.RingResult {
			metadataLock, err := AcquireTaskLock(
				sender.StateDir,
				sender.TaskID,
				100*time.Millisecond,
			)
			if err != nil {
				return herdr.RingResult{Outcome: herdr.RingFailed, Err: err}
			}
			if err := metadataLock.Release(); err != nil {
				return herdr.RingResult{Outcome: herdr.RingFailed, Err: err}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err = acquireLeaseCommandLock(
				ctx,
				filepath.Join(sender.StateDir.String(), ".fm-lease-command.lock"),
			)
			if !errors.Is(err, context.DeadlineExceeded) {
				return herdr.RingResult{
					Outcome: herdr.RingFailed,
					Err:     errors.New("lease guard released before ring"),
				}
			}
			return herdr.RingResult{Outcome: herdr.RingSent}
		},
	)
	result, err := sender.Send(context.Background(), "hello")
	if err != nil || result.Doorbell.Outcome != herdr.RingSent {
		t.Fatalf("send = %+v, %v", result, err)
	}
}

func TestSenderBashSequenceParity(t *testing.T) {
	source := os.Getenv("FIRSTMATE_SOURCE")
	if source == "" {
		t.Skip("set FIRSTMATE_SOURCE to run Bash interoperability test")
	}
	sender, inbox, _ := newTestSender(t)
	library := filepath.Join(source, "bin", "fm-task-inbox-lib.sh")
	if _, err := os.Stat(library); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		"bash",
		"-c",
		". \"$1\"; fm_task_inbox_write \"$2\" task-1 bash",
		"bash",
		library,
		sender.StateDir.String(),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Bash write: %v: %s", err, output)
	}
	result, err := sender.Send(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordPath.String() != filepath.Join(inbox, "002.msg") {
		t.Fatalf("Go sequence after Bash = %q", result.RecordPath.String())
	}
	read := exec.Command(
		"bash",
		"-c",
		". \"$1\"; fm_task_inbox_body \"$2\"",
		"bash",
		library,
		result.RecordPath.String(),
	)
	if output, err := read.CombinedOutput(); err != nil || string(output) != "go" {
		t.Fatalf("Bash read Go record: %q, %v", output, err)
	}
}

func TestSenderRejectsMetadataChangeWhileWaitingForLease(t *testing.T) {
	sender, inbox, _ := newTestSender(t)
	leaseLock, err := acquireLeaseCommandLock(
		context.Background(),
		filepath.Join(sender.StateDir.String(), ".fm-lease-command.lock"),
	)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	results := make(chan error, 1)
	go func() { close(started); _, err := sender.Send(context.Background(), "hello"); results <- err }()
	<-started
	time.Sleep(150 * time.Millisecond)
	changed := strings.Replace(validMetadata, "spawn_gen=s123.456.7", "spawn_gen=s123.456.8", 1)
	if err := os.WriteFile(filepath.Join(sender.StateDir.String(), "task-1.meta"), []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := leaseLock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-results; !errors.Is(err, ErrTaskChanged) {
		t.Fatalf("changed metadata during lease wait = %v", err)
	}
	if _, err := os.Stat(inbox); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inbox = %v", err)
	}
}

func TestSenderMetadataLockManualCancellation(t *testing.T) {
	sender, inbox, _ := newTestSender(t)
	metadataLock, err := AcquireTaskLock(sender.StateDir, sender.TaskID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer metadataLock.Release()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := sender.Send(ctx, "hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled metadata wait = %v", err)
	}
	if _, err := os.Stat(inbox); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inbox = %v", err)
	}
}

func TestSenderSequenceLockTimeoutLeavesNoRecord(t *testing.T) {
	sender, inbox, _ := newTestSender(t)
	if err := os.MkdirAll(filepath.Join(inbox, "handled"), 0700); err != nil {
		t.Fatal(err)
	}
	sequenceLock, err := acquireLeaseCommandLock(
		context.Background(),
		filepath.Join(inbox, ".seq.lock"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer sequenceLock.Release()
	start := time.Now()
	if _, err := sender.Send(context.Background(), "hello"); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("sequence lock wait = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatalf("sequence lock wait = %v", elapsed)
	}
	entries, err := os.ReadDir(inbox)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".msg") {
			t.Fatalf("unexpected record %s", entry.Name())
		}
	}
}

func TestSenderMetadataChangesBeforeDoorbell(t *testing.T) {
	sender, _, doorbell := newTestSender(t)
	sender.Clock = func() time.Time {
		updated := strings.Replace(validMetadata, "spawn_gen=s123.456.7", "spawn_gen=s123.456.8", 1)
		if err := os.WriteFile(filepath.Join(sender.StateDir.String(), "task-1.meta"), []byte(updated), 0600); err != nil {
			t.Fatal(err)
		}
		return time.Unix(1000, 0)
	}
	result, err := sender.Send(context.Background(), "hello")
	if err != nil || result.RecordPath.String() == "" ||
		result.Doorbell.Outcome != herdr.RingFailed ||
		!errors.Is(result.Doorbell.Err, ErrTaskChanged) {
		t.Fatalf("send = %+v, %v", result, err)
	}
	if doorbell.calls != 0 {
		t.Fatalf("ring calls=%d", doorbell.calls)
	}
	if _, err := os.Stat(result.RecordPath.String()); err != nil {
		t.Fatalf("durable record: %v", err)
	}
}

func TestSenderDoorbellOutcomesKeepRecord(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome herdr.RingOutcome
	}{
		{"sent", herdr.RingSent},
		{"composer skipped", herdr.RingSkipped},
		{"failed", herdr.RingFailed},
		{"unavailable", herdr.RingUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender, _, doorbell := newTestSender(t)
			doorbell.result = herdr.RingResult{Outcome: test.outcome}
			result, err := sender.Send(context.Background(), "hello")
			if err != nil || result.Doorbell.Outcome != test.outcome || doorbell.calls != 1 {
				t.Fatalf("send=%+v err=%v calls=%d", result, err, doorbell.calls)
			}
			if _, err := os.Stat(result.RecordPath.String()); err != nil {
				t.Fatalf("durable record: %v", err)
			}
		})
	}
}

func TestSenderWithoutDoorbellReportsNotAttempted(t *testing.T) {
	sender, _, _ := newTestSender(t)
	sender.Doorbell = nil
	result, err := sender.Send(context.Background(), "hello")
	if err != nil || result.Doorbell.Outcome != herdr.RingNotAttempted ||
		!errors.Is(result.Doorbell.Err, ErrNoDoorbell) {
		t.Fatalf("send=%+v err=%v", result, err)
	}
	if _, err := os.Stat(result.RecordPath.String()); err != nil {
		t.Fatalf("durable record: %v", err)
	}
}
