package primary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testIdentity(kind, session string, pid int) Identity {
	return Identity{Kind: kind, SessionID: session, PID: pid, StartTicks: "10", BootID: "boot"}
}

func TestSameLiveProcess(t *testing.T) {
	identity := testIdentity("claude", "session", 10)
	process := Process{PID: 10, Kind: "claude", StartTicks: "10"}
	for _, check := range []struct {
		name    string
		bootID  string
		process Process
		want    bool
	}{
		{"same process", "boot", process, true},
		{"other boot", "other", process, false},
		{"other pid", "boot", Process{PID: 11, Kind: "claude", StartTicks: "10"}, false},
		{"reused pid", "boot", Process{PID: 10, Kind: "claude", StartTicks: "11"}, false},
		{"replaced program", "boot", Process{PID: 10, Kind: "codex", StartTicks: "10"}, false},
	} {
		t.Run(check.name, func(t *testing.T) {
			if got := sameLiveProcess(identity, check.bootID, check.process); got != check.want {
				t.Fatalf("sameLiveProcess() = %t, want %t", got, check.want)
			}
		})
	}
}

func TestClaimKeepsOneLiveProcessAndRecoversStaleOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	first := testIdentity("claude", "shared-session", 10)
	otherProcess := testIdentity("claude", "shared-session", 20)
	second := testIdentity("codex", "second", 30)
	live := func(identity Identity) (bool, error) { return identity.StartTicks == "10", nil }

	for _, check := range []struct {
		name     string
		identity Identity
		want     Status
	}{
		{"first claim", first, Owner},
		{"same process", first, Owner},
		{"resumed session in another process", otherProcess, Held},
		{"other live session", second, Held},
	} {
		t.Run(check.name, func(t *testing.T) {
			got, err := claimWithProbe(root, check.identity, live)
			if err != nil || got != check.want {
				t.Fatalf("Claim() = %q, %v; want %q", got, err, check.want)
			}
		})
	}
	if got, err := verifyWithProbe(root, otherProcess, live); err != nil || got != Held {
		t.Fatalf("Verify(other process) = %q, %v; want held", got, err)
	}
	if got, err := verifyWithProbe(root, first, live); err != nil || got != Owner {
		t.Fatalf("Verify(first) = %q, %v; want owner", got, err)
	}

	stale := first
	stale.StartTicks = "0"
	if err := writeOwner(root, stale); err != nil {
		t.Fatal(err)
	}
	if got, err := claimWithProbe(root, second, live); err != nil || got != Owner {
		t.Fatalf("Claim(stale) = %q, %v; want owner", got, err)
	}
}

func TestClaimRejectsMalformedOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "primary.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := claimWithProbe(
		root,
		testIdentity("claude", "test", 10),
		func(Identity) (bool, error) { return true, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "read primary owner") {
		t.Fatalf("Claim() error = %v; want malformed owner error", err)
	}
}

func TestWithLockSerializesActions(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := withLock(root, func() (Status, error) {
			close(firstEntered)
			<-releaseFirst
			return Owner, nil
		})
		firstDone <- err
	}()
	<-firstEntered
	secondStarted := make(chan struct{})
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		_, err := withLock(root, func() (Status, error) {
			close(secondEntered)
			return Held, nil
		})
		secondDone <- err
	}()
	<-secondStarted
	select {
	case <-secondEntered:
		t.Fatal("second action entered while first action held the lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second action did not enter after first released the lock")
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}
