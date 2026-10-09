package firstmate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/herdr"
)

type checkHealth struct {
	states  []herdr.HealthStatus
	targets []herdr.Target
	hook    func()
}

func (health *checkHealth) Health(_ context.Context, target herdr.Target) herdr.HealthStatus {
	health.targets = append(health.targets, target)
	if health.hook != nil {
		health.hook()
	}
	state := health.states[0]
	health.states = health.states[1:]
	return state
}

type checkDoorbell struct {
	hook   func()
	calls  int
	target herdr.Target
	text   string
	result herdr.RingResult
}

func (ringer *checkDoorbell) Ring(
	_ context.Context,
	target herdr.Target,
	text string,
) herdr.RingResult {
	ringer.calls++
	if ringer.hook != nil {
		ringer.hook()
	}
	ringer.target = target
	ringer.text = text
	return ringer.result
}
func checkFixture(t *testing.T) (RetryCheck, string, RecordName) {
	t.Helper()
	sender, inbox, _ := newTestSender(t)
	store, err := NewRetryStore(sender.StateDir, sender.TaskID, NewRetryPolicy("90", "3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	name, _ := ParseRecordName("001.msg")
	return RetryCheck{StateDir: sender.StateDir, TaskID: sender.TaskID, Store: store}, inbox, name
}
func TestRetryCheckOutcomes(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, test := range []struct {
		name               string
		age                int64
		ladder             string
		health             []herdr.HealthStatus
		delivery           herdr.RingOutcome
		ringErr            error
		want               CheckOutcome
		calls, healthCalls int
		attempts           string
	}{
		{name: "quiet before grace", age: 950, want: CheckQuiet},
		{name: "busy ring wait", age: 800, health: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthBusy}}, want: CheckWait, healthCalls: 1},
		{name: "busy spent budget wait", age: 800, ladder: "001.msg\t3\t0\n", health: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthBusy}}, want: CheckWait, healthCalls: 1, attempts: "001.msg\t3\t0\n"},
		{name: "idle ring", age: 800, health: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthIdle}, {Endpoint: herdr.Alive, Activity: herdr.HealthIdle}}, delivery: herdr.RingSent, want: CheckRung, calls: 1, healthCalls: 2, attempts: "001.msg\t1\t1000\n"},
		{name: "unreadable failed ring", age: 800, health: []herdr.HealthStatus{{Endpoint: herdr.Unreadable}, {Endpoint: herdr.Unreadable}}, delivery: herdr.RingFailed, ringErr: errors.New("send failed"), want: CheckRung, calls: 1, healthCalls: 2, attempts: "001.msg\t1\t1000\n"},
		{name: "composer skip", age: 800, health: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthIdle}, {Endpoint: herdr.Alive, Activity: herdr.HealthIdle}}, delivery: herdr.RingSkipped, want: CheckRung, calls: 1, healthCalls: 2, attempts: "001.msg\t1\t1000\n"},
		{name: "gone during ring", age: 800, health: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthIdle}, {Endpoint: herdr.Alive, Activity: herdr.HealthIdle}}, delivery: herdr.RingUnavailable, want: CheckUnavailable, calls: 1, healthCalls: 2},
		{name: "dead direct", age: 800, health: []herdr.HealthStatus{{Endpoint: herdr.Dead, Activity: herdr.HealthBusy}}, want: CheckUnavailable, healthCalls: 1},
		{name: "missing direct", age: 800, health: []herdr.HealthStatus{{Endpoint: herdr.Missing, Activity: herdr.HealthBusy}}, want: CheckUnavailable, healthCalls: 1},
		{name: "dead after first health", age: 800, health: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthIdle}, {Endpoint: herdr.Dead}}, want: CheckUnavailable, healthCalls: 2},
		{name: "spent idle escalates", age: 800, ladder: "001.msg\t3\t0\n", health: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthIdle}}, want: CheckEscalate, healthCalls: 1, attempts: "001.msg\t3\t0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			check, inbox, name := checkFixture(t)
			writeRetryRecord(t, inbox, name.String(), "", time.Unix(test.age, 0))
			if test.ladder != "" {
				if err := os.WriteFile(filepath.Join(inbox, ".ring-state"), []byte(test.ladder), 0600); err != nil {
					t.Fatal(err)
				}
			}
			health := &checkHealth{states: test.health}
			ring := &checkDoorbell{
				result: herdr.RingResult{Outcome: test.delivery, Err: test.ringErr},
			}
			check.Health = health
			check.Doorbell = ring
			got, err := check.Run(context.Background(), now)
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(got.Delivery.Err, test.ringErr) {
				t.Fatalf("delivery error = %v, want %v", got.Delivery.Err, test.ringErr)
			}
			if ring.calls == 0 && got.Delivery.Outcome != herdr.RingNotAttempted {
				t.Fatalf("delivery = %+v without a ring", got.Delivery)
			}
			if ring.calls > 0 && got.Outcome == CheckRung && got.Delivery.Outcome != test.delivery {
				t.Fatalf("delivery outcome = %v, want %v", got.Delivery.Outcome, test.delivery)
			}
			if got.Outcome == CheckRung && got.Attempts != 1 {
				t.Fatalf("recorded attempts=%d, want 1", got.Attempts)
			}
			if got.Outcome != test.want || ring.calls != test.calls ||
				len(health.targets) != test.healthCalls {
				t.Fatalf("result=%+v ring=%d health=%d", got, ring.calls, len(health.targets))
			}
			for _, target := range health.targets {
				if target.Session() != "crew" || target.PaneID() != "pane-1" {
					t.Fatalf("target=%+v", target)
				}
			}
			if ring.calls > 0 &&
				(ring.target.Session() != "crew" || ring.target.PaneID() != "pane-1" || ring.text != doorbellText(check.TaskID)) {
				t.Fatalf("ring target=%+v text=%q", ring.target, ring.text)
			}
			body, readErr := os.ReadFile(filepath.Join(inbox, ".ring-state"))
			if test.attempts == "" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("state=%q,%v", body, readErr)
				}
			} else if readErr != nil || string(body) != test.attempts {
				t.Fatalf("state=%q,%v", body, readErr)
			}
		})
	}
}

func TestRetryCheckEscalationPublication(t *testing.T) {
	for _, test := range []struct {
		name          string
		publish       bool
		fail          bool
		wantPublished int
	}{
		{name: "no publisher"}, {name: "published", publish: true, wantPublished: 1}, {name: "publisher failure", publish: true, fail: true, wantPublished: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			check, inbox, name := checkFixture(t)
			now := time.Unix(1000, 0)
			writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
			if err := os.WriteFile(filepath.Join(inbox, ".ring-state"), []byte("001.msg\t3\t0\n"), 0600); err != nil {
				t.Fatal(err)
			}
			check.Health = &checkHealth{
				states: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthIdle}},
			}
			marker := filepath.Join(inbox, ".escalated")
			published := 0
			publishErr := errors.New("publish failed")
			if test.publish {
				check.Publish = func(_ context.Context, escalation Escalation) error {
					published++
					if escalation.TaskID != check.TaskID || escalation.Record != name ||
						escalation.Attempts != 3 ||
						escalation.Outcome != CheckEscalate {
						t.Fatalf("escalation=%+v", escalation)
					}
					if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("marker written before publish: %v", err)
					}
					if test.fail {
						return publishErr
					}
					return nil
				}
			}
			result, err := check.Run(context.Background(), now)
			if test.fail {
				if !errors.Is(err, publishErr) {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != CheckEscalate || result.Attempts != 3 ||
				published != test.wantPublished {
				t.Fatalf("result=%+v published=%d", result, published)
			}
			body, readErr := os.ReadFile(marker)
			if test.publish && !test.fail {
				if readErr != nil || string(body) != "001.msg\n" {
					t.Fatalf("marker=%q,%v", body, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("unexpected marker=%q,%v", body, readErr)
			}
		})
	}
}

func TestRetryCheckAckAndChangedMetadataRaces(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(t *testing.T, inbox, state string)
		want    CheckOutcome
		wantErr error
	}{
		{name: "ack during health", mutate: func(t *testing.T, inbox, _ string) {
			if err := os.Remove(filepath.Join(inbox, "001.msg")); err != nil {
				t.Fatal(err)
			}
		}, want: CheckQuiet},
		{name: "changed generation", mutate: func(t *testing.T, _, state string) {
			changed := "spawn_gen=s123.456.7"
			updated := "spawn_gen=s123.456.8"
			body := []byte(strings.Replace(validMetadata, changed, updated, 1))
			if err := os.WriteFile(filepath.Join(state, "task-1.meta"), body, 0600); err != nil {
				t.Fatal(err)
			}
		}, wantErr: ErrTaskChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			check, inbox, name := checkFixture(t)
			writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
			health := &checkHealth{
				states: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthIdle}},
			}
			health.hook = func() { test.mutate(t, inbox, check.StateDir.String()) }
			check.Health = health
			ring := &checkDoorbell{}
			check.Doorbell = ring
			result, err := check.Run(context.Background(), time.Unix(1000, 0))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if test.wantErr == nil && result.Outcome != test.want {
				t.Fatalf("result=%+v", result)
			}
			if ring.calls != 0 {
				t.Fatalf("ring calls=%d", ring.calls)
			}
			if _, err := os.Stat(filepath.Join(inbox, ".ring-state")); !errors.Is(
				err,
				os.ErrNotExist,
			) {
				t.Fatalf("attempt recorded: %v", err)
			}
		})
	}
}

func TestRetryCheckAckDuringRing(t *testing.T) {
	check, inbox, name := checkFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	check.Health = &checkHealth{
		states: []herdr.HealthStatus{
			{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
			{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
		},
	}
	ring := &checkDoorbell{result: herdr.RingResult{Outcome: herdr.RingSent}, hook: func() {
		if err := os.Remove(filepath.Join(inbox, name.String())); err != nil {
			t.Fatal(err)
		}
	}}
	check.Doorbell = ring
	result, err := check.Run(context.Background(), time.Unix(1000, 0))
	if err != nil || result.Outcome != CheckQuiet || ring.calls != 1 {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, ring.calls)
	}
	if _, err := os.Stat(filepath.Join(inbox, ".ring-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt=%v", err)
	}
}

func TestRetryCheckUnwritableAttempt(t *testing.T) {
	check, inbox, name := checkFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	if err := os.Mkdir(filepath.Join(inbox, ".ring-state"), 0700); err != nil {
		t.Fatal(err)
	}
	check.Health = &checkHealth{
		states: []herdr.HealthStatus{
			{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
			{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
		},
	}
	check.Doorbell = &checkDoorbell{result: herdr.RingResult{Outcome: herdr.RingSent}}
	_, err := check.Run(context.Background(), time.Unix(1000, 0))
	if err == nil {
		t.Fatal("unwritable attempt was ignored")
	}
}

func TestRetryCheckRejectsMismatchedStoreBeforeEffects(t *testing.T) {
	first, inbox, name := checkFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	second, _, _ := checkFixture(t)
	second.Store = first.Store
	health := &checkHealth{
		states: []herdr.HealthStatus{{Endpoint: herdr.Alive, Activity: herdr.HealthIdle}},
	}
	ring := &checkDoorbell{}
	second.Health = health
	second.Doorbell = ring
	_, err := second.Run(context.Background(), time.Unix(1000, 0))
	if !errors.Is(err, ErrRetryStoreMismatch) {
		t.Fatalf("mismatched store error=%v", err)
	}
	if len(health.targets) != 0 || ring.calls != 0 {
		t.Fatalf("health=%d ring=%d", len(health.targets), ring.calls)
	}
	if _, err := os.Stat(filepath.Join(inbox, ".ring-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("foreign state mutated: %v", err)
	}
}

func TestRetryCheckUnavailableRingPublishesWithoutAttempt(t *testing.T) {
	check, inbox, name := checkFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	check.Health = &checkHealth{
		states: []herdr.HealthStatus{
			{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
			{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
		},
	}
	check.Doorbell = &checkDoorbell{
		result: herdr.RingResult{
			Outcome: herdr.RingUnavailable,
			Err:     errors.New("endpoint vanished"),
		},
	}
	published := 0
	check.Publish = func(_ context.Context, escalation Escalation) error {
		published++
		if escalation.Record != name || escalation.Attempts != 0 ||
			escalation.Outcome != CheckUnavailable {
			t.Fatalf("escalation=%+v", escalation)
		}
		if _, err := os.Stat(filepath.Join(inbox, ".escalated")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("marker before publish: %v", err)
		}
		return nil
	}
	result, err := check.Run(context.Background(), time.Unix(1000, 0))
	if err != nil || result.Outcome != CheckUnavailable || published != 1 ||
		result.Delivery.Outcome != herdr.RingUnavailable {
		t.Fatalf("result=%+v err=%v published=%d", result, err, published)
	}
	if _, err := os.Stat(filepath.Join(inbox, ".ring-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt state=%v", err)
	}
	body, err := os.ReadFile(filepath.Join(inbox, ".escalated"))
	if err != nil || string(body) != "001.msg\n" {
		t.Fatalf("marker=%q,%v", body, err)
	}
}

func TestRetryCheckMetadataChangesDuringRing(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome herdr.RingOutcome
	}{
		{"sent", herdr.RingSent},
		{"unavailable", herdr.RingUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			check, inbox, name := checkFixture(t)
			writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
			check.Health = &checkHealth{states: []herdr.HealthStatus{
				{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
				{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
			}}
			ring := &checkDoorbell{result: herdr.RingResult{Outcome: test.outcome}}
			ring.hook = func() {
				updated := strings.Replace(
					validMetadata,
					"spawn_gen=s123.456.7",
					"spawn_gen=s123.456.8",
					1,
				)
				if err := os.WriteFile(filepath.Join(check.StateDir.String(), "task-1.meta"), []byte(updated), 0600); err != nil {
					t.Fatal(err)
				}
			}
			check.Doorbell = ring
			published := 0
			check.Publish = func(context.Context, Escalation) error { published++; return nil }
			_, err := check.Run(context.Background(), time.Unix(1000, 0))
			if !errors.Is(err, ErrTaskChanged) || ring.calls != 1 || published != 0 {
				t.Fatalf("err=%v rings=%d published=%d", err, ring.calls, published)
			}
			for _, name := range []string{".ring-state", ".escalated"} {
				if _, err := os.Stat(filepath.Join(inbox, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s mutated: %v", name, err)
				}
			}
		})
	}
}

func TestRetryCheckNotAttemptedDoesNotCount(t *testing.T) {
	check, inbox, name := checkFixture(t)
	writeRetryRecord(t, inbox, name.String(), "", time.Unix(800, 0))
	check.Health = &checkHealth{states: []herdr.HealthStatus{
		{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
		{Endpoint: herdr.Alive, Activity: herdr.HealthIdle},
	}}
	check.Doorbell = &checkDoorbell{result: herdr.RingResult{Outcome: herdr.RingNotAttempted}}
	result, err := check.Run(context.Background(), time.Unix(1000, 0))
	if !errors.Is(err, ErrRingNotAttempted) || result.Delivery.Outcome != herdr.RingNotAttempted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(inbox, ".ring-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt recorded: %v", err)
	}
}
