package firstmate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func retryFixture(t *testing.T) (RetryStore, string, RecordName) {
	t.Helper()
	state, id, _ := testLockInputs(t)
	store, err := NewRetryStore(state, id, NewRetryPolicy("90", "3"))
	if err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(state.String(), id.String()+".inbox")
	if err := os.MkdirAll(filepath.Join(inbox, "handled"), 0700); err != nil {
		t.Fatal(err)
	}
	name, _ := ParseRecordName("001.msg")
	return store, inbox, name
}
func writeRetryRecord(t *testing.T, inbox, name, delivery string, when time.Time) {
	t.Helper()
	body := "schema=fm-task-inbox.v1\nat=1970-01-01T00:00:00Z\n"
	if delivery != "" {
		body += "delivery=" + delivery + "\n"
	}
	body += "--\nbody"
	path := filepath.Join(inbox, name)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}
func TestRetryStoreSelectsOldestOrdinary(t *testing.T) {
	store, inbox, _ := retryFixture(t)
	now := time.Unix(1000, 0)
	writeRetryRecord(t, inbox, "003.msg", "", time.Unix(800, 0))
	writeRetryRecord(t, inbox, "002.msg", "fire-and-forget", time.Unix(800, 0))
	malformed := filepath.Join(inbox, "001.msg")
	if err := os.WriteFile(malformed, []byte("older unknown header"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(malformed, time.Unix(800, 0), time.Unix(800, 0)); err != nil {
		t.Fatal(err)
	}
	decision, err := store.Due(now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action() != RetryRing || decision.RecordName().String() != "001.msg" {
		t.Fatalf("due = %v %q", decision.Action(), decision.RecordName())
	}
}
func TestRetryStoreReadsMalformedStateAndEscalation(t *testing.T) {
	for _, test := range []struct {
		name, state, marker string
		want                RetryAction
		count               uint64
	}{
		{"missing", "", "", RetryRing, 0},
		{"malformed count", "001.msg\tbad\t999\n", "", RetryQuiet, 0},
		{"malformed epoch", "001.msg\t2\tbad\n", "", RetryRing, 2},
		{"matching marker", "001.msg\t3\t999\n", "001.msg\n", RetryQuiet, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, inbox, _ := retryFixture(t)
			writeRetryRecord(t, inbox, "001.msg", "", time.Unix(800, 0))
			if test.state != "" {
				if err := os.WriteFile(filepath.Join(inbox, ".ring-state"), []byte(test.state), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if test.marker != "" {
				if err := os.WriteFile(filepath.Join(inbox, ".escalated"), []byte(test.marker), 0600); err != nil {
					t.Fatal(err)
				}
			}
			decision, err := store.Due(time.Unix(1000, 0))
			if err != nil {
				t.Fatal(err)
			}
			if decision.Action() != test.want || decision.NextState().Attempts() != test.count {
				t.Fatalf("due = %v, count %d", decision.Action(), decision.NextState().Attempts())
			}
		})
	}
}
func TestRetryStoreRecordAttemptExactAndRestart(t *testing.T) {
	store, inbox, name := retryFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	if err := store.RecordAttempt(name, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAttempt(name, time.Unix(1100, 0)); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(inbox, ".ring-state")
	if info, err := os.Stat(statePath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state mode = %v, %v", info, err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil || string(data) != "001.msg\t2\t1100\n" {
		t.Fatalf("state = %q, %v", data, err)
	}
	decision, err := store.Due(time.Unix(1101, 0))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action() != RetryQuiet || decision.NextState().Attempts() != 2 {
		t.Fatalf("restart = %v, %d", decision.Action(), decision.NextState().Attempts())
	}
}
func TestRetryStoreClearsChangedAndEmptyInbox(t *testing.T) {
	store, inbox, _ := retryFixture(t)
	writeRetryRecord(t, inbox, "002.msg", "", time.Unix(800, 0))
	for path, body := range map[string]string{".ring-state": "001.msg\t3\t999\n", ".escalated": "001.msg\n"} {
		if err := os.WriteFile(filepath.Join(inbox, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	decision, err := store.Due(time.Unix(1000, 0))
	if err != nil || decision.Action() != RetryRing || !decision.ClearBookkeeping() {
		t.Fatalf("changed = %v, %v", decision, err)
	}
	for _, name := range []string{".ring-state", ".escalated"} {
		if _, err := os.Stat(filepath.Join(inbox, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale %s remains: %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(inbox, "002.msg")); err != nil {
		t.Fatal(err)
	}
	decision, err = store.Due(time.Unix(1000, 0))
	if err != nil || decision.Action() != RetryQuiet || !decision.ClearBookkeeping() {
		t.Fatalf("empty = %v, %v", decision, err)
	}
}
func TestRetryStoreAckRaceAndWriteFailure(t *testing.T) {
	store, inbox, name := retryFixture(t)
	if err := store.RecordAttempt(name, time.Unix(1000, 0)); err != nil {
		t.Fatalf("acked attempt = %v", err)
	}
	if err := store.MarkEscalated(name); err != nil {
		t.Fatalf("acked escalation = %v", err)
	}
	if _, err := os.Stat(filepath.Join(inbox, ".ring-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acked state = %v", err)
	}
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	if err := os.Mkdir(filepath.Join(inbox, ".ring-state"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAttempt(name, time.Unix(1000, 0)); err == nil {
		t.Fatal("pending write failure was hidden")
	}
	if err := os.Remove(filepath.Join(inbox, name.String())); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAttempt(name, time.Unix(1000, 0)); err != nil {
		t.Fatalf("acked after write failure = %v", err)
	}
}
func TestRetryStorePublishesBeforeMarker(t *testing.T) {
	store, inbox, name := retryFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	failed := errors.New("wake failed")
	if err := store.PublishEscalation(name, func() error { return failed }); !errors.Is(
		err,
		failed,
	) {
		t.Fatalf("publish failure = %v", err)
	}
	marker := filepath.Join(inbox, ".escalated")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker before publish = %v", err)
	}
	if err := store.PublishEscalation(name, func() error {
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("early marker = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "001.msg\n" {
		t.Fatalf("marker = %q, %v", data, err)
	}
}

func TestRetryStoreDoesNotRecordFireAndForget(t *testing.T) {
	store, inbox, name := retryFixture(t)
	writeRetryRecord(t, inbox, name.String(), "fire-and-forget", time.Unix(800, 0))
	if err := store.RecordAttempt(name, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inbox, ".ring-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fire-and-forget ladder = %v", err)
	}
}
func TestRetryStoreZeroValueRejectsMutation(t *testing.T) {
	name, _ := ParseRecordName("001.msg")
	if _, err := (RetryStore{}).Due(time.Now()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("zero store Due = %v", err)
	}
	if err := (RetryStore{}).RecordAttempt(name, time.Now()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("zero store attempt = %v", err)
	}
	if err := (RetryStore{}).MarkEscalated(name); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("zero store marker = %v", err)
	}
}

func TestRetryStoreBashWireParity(t *testing.T) {
	source := os.Getenv("FIRSTMATE_SOURCE")
	if source == "" {
		t.Skip("set FIRSTMATE_SOURCE to run Bash interoperability test")
	}
	library := filepath.Join(source, "bin", "fm-task-inbox-lib.sh")
	if _, err := os.Stat(library); err != nil {
		t.Fatal(err)
	}
	store, inbox, name := retryFixture(t)
	old := time.Now().Add(-200 * time.Second).Truncate(time.Second)
	writeRetryRecord(t, inbox, name.String(), "", old)
	state := filepath.Dir(inbox)
	bashAttempt := exec.Command(
		"bash",
		"-c",
		`. "$1"; fm_task_inbox_record_ring "$2" task-1 "$3"`,
		"bash",
		library,
		state,
		filepath.Join(inbox, name.String()),
	)
	if output, err := bashAttempt.CombinedOutput(); err != nil {
		t.Fatalf("Bash attempt: %v: %s", err, output)
	}
	decision, err := store.Due(time.Now())
	if err != nil || decision.NextState().Attempts() != 1 || decision.Action() != RetryQuiet {
		t.Fatalf("Go read Bash state = %v, %v", decision, err)
	}
	if err := store.RecordAttempt(name, old); err != nil {
		t.Fatal(err)
	}
	bashDue := exec.Command(
		"bash",
		"-c",
		`. "$1"; fm_task_inbox_due_action "$2" task-1`,
		"bash",
		library,
		state,
	)
	output, err := bashDue.CombinedOutput()
	if err != nil || string(output) != "ring "+filepath.Join(inbox, name.String()) {
		t.Fatalf("Bash read Go state = %q, %v", output, err)
	}
}

func TestRetryStorePublishMarkerFailureIsActionable(t *testing.T) {
	store, inbox, name := retryFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	if err := os.Mkdir(filepath.Join(inbox, ".escalated"), 0700); err != nil {
		t.Fatal(err)
	}
	published := false
	if err := store.PublishEscalation(name, func() error { published = true; return nil }); err == nil ||
		!published {
		t.Fatalf("marker failure = %v, published = %v", err, published)
	}
}
func TestRetryStoreAckDuringPublicationSkipsMarker(t *testing.T) {
	store, inbox, name := retryFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	if err := store.PublishEscalation(name, func() error { return os.Remove(filepath.Join(inbox, name.String())) }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inbox, ".escalated")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ack marker = %v", err)
	}
}

func TestRetryStoreClearsBookkeepingAfterLastAck(t *testing.T) {
	store, inbox, name := retryFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	for file, body := range map[string]string{".ring-state": "001.msg\t2\t900\n", ".escalated": "001.msg\n"} {
		if err := os.WriteFile(filepath.Join(inbox, file), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(inbox, name.String())); err != nil {
		t.Fatal(err)
	}
	decision, err := store.Due(time.Unix(1000, 0))
	if err != nil || decision.Action() != RetryQuiet || !decision.ClearBookkeeping() {
		t.Fatalf("acked Due = %v, %v", decision, err)
	}
	for _, file := range []string{".ring-state", ".escalated"} {
		if _, err := os.Stat(filepath.Join(inbox, file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("acked %s remains: %v", file, err)
		}
	}
}
