package primary

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testIdentity(kind, session string, pid int) Identity {
	return Identity{
		Version:      2,
		Kind:         kind,
		SessionID:    session,
		PID:          pid,
		StartTicks:   "10",
		BootID:       "boot",
		PIDNamespace: "pid:[1]",
	}
}

func TestLegacyOwnerLifecycle(t *testing.T) {
	owner := testIdentity("codex", "owner", 1)
	legacy := owner
	legacy.Version = 0
	legacy.PIDNamespace = ""
	actions := []struct {
		name string
		run  func(string, Identity) (Status, error)
		want Status
	}{
		{"claim", func(root string, identity Identity) (Status, error) {
			return claimWithProbe(root, identity, nil)
		}, Owner},
		{"verify", func(root string, identity Identity) (Status, error) {
			return verifyWithProbe(root, identity, nil)
		}, Owner},
		{"release", Release, Vacant},
	}
	for _, action := range actions {
		for _, match := range []bool{false, true} {
			name := action.name + "/other"
			if match {
				name = action.name + "/owner"
			}
			t.Run(name, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), ".concierge")
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(legacy)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "primary.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				candidate := testIdentity("claude", "other", 2)
				want := Held
				if match {
					candidate = owner
					want = action.want
				}
				status, err := action.run(root, candidate)
				if err != nil || status != want {
					t.Fatalf("%s = %q, %v; want %q", action.name, status, err, want)
				}
				stored, err := readOwner(root)
				if match && action.name == "release" {
					if !os.IsNotExist(err) {
						t.Fatalf("released owner still exists: %+v, %v", stored, err)
					}
				} else {
					wantOwner := legacy
					if match {
						wantOwner = owner
					}
					if err != nil || stored != wantOwner {
						t.Fatalf("stored owner = %+v, %v; want %+v", stored, err, wantOwner)
					}
				}
			})
		}
	}
}

func TestOwnerInOtherNamespaceRemainsHeld(t *testing.T) {
	identity := testIdentity("codex", "owner", 1)
	called := false
	live, err := checkOwnerLive(identity, "boot", "pid:[2]", func(int) (Process, error) {
		called = true
		return Process{}, os.ErrNotExist
	})
	if err != nil || !live || called {
		t.Fatalf("checkOwnerLive() = %t, %v; lookup called %t", live, err, called)
	}
}

func TestReleaseOnlyRecordedOwner(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	owner := testIdentity("codex", "owner", 1)
	other := testIdentity("codex", "owner", 2)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(root, owner); err != nil {
		t.Fatal(err)
	}
	if status, err := Release(root, other); err != nil || status != Held {
		t.Fatalf("Release(other) = %q, %v", status, err)
	}
	if status, err := Release(root, owner); err != nil || status != Vacant {
		t.Fatalf("Release(owner) = %q, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(root, "primary.json")); !os.IsNotExist(err) {
		t.Fatalf("owner record still exists: %v", err)
	}
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

func TestCodexOwnershipFollowsProcessAcrossSessionIDs(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	first := testIdentity("codex", "unverified-session", 10)
	current := testIdentity("codex", "active-session", 10)
	other := testIdentity("codex", "active-session", 20)
	live := func(Identity) (bool, error) { return true, nil }
	if got, err := claimWithProbe(root, first, live); err != nil || got != Owner {
		t.Fatalf("first claim = %q, %v; want owner", got, err)
	}
	if got, err := claimWithProbe(root, current, live); err != nil || got != Owner {
		t.Fatalf("current claim = %q, %v; want owner", got, err)
	}
	if got, err := verifyWithProbe(root, current, live); err != nil || got != Owner {
		t.Fatalf("current verify = %q, %v; want owner", got, err)
	}
	if got, err := verifyWithProbe(root, other, live); err != nil || got != Held {
		t.Fatalf("other verify = %q, %v; want held", got, err)
	}
	for _, check := range []struct {
		name   string
		change func(*Identity)
	}{
		{"other PID", func(identity *Identity) { identity.PID = 20 }},
		{"reused PID", func(identity *Identity) { identity.StartTicks = "20" }},
		{"other boot", func(identity *Identity) { identity.BootID = "other-boot" }},
		{"other namespace", func(identity *Identity) { identity.PIDNamespace = "pid:[2]" }},
	} {
		t.Run(check.name, func(t *testing.T) {
			candidate := current
			check.change(&candidate)
			if got, err := Release(root, candidate); err != nil || got != Held {
				t.Fatalf("release = %q, %v; want held", got, err)
			}
		})
	}
	if got, err := Release(root, current); err != nil || got != Vacant {
		t.Fatalf("current release = %q, %v; want vacant", got, err)
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
