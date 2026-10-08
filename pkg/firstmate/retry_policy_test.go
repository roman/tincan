package firstmate

import (
	"testing"
	"time"
)

func TestRetryPolicyConfiguration(t *testing.T) {
	for _, test := range []struct {
		name               string
		grace, max         string
		wantGrace, wantMax uint64
	}{
		{"defaults", "", "", 90, 3},
		{"invalid", "1.5", "three", 90, 3},
		{"negative", "-1", "-2", 90, 3},
		{"overflow", "999999999999999999999", "999999999999999999999", 90, 3},
		{"zero", "0", "0", 0, 0},
		{"custom", "12", "5", 12, 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := NewRetryPolicy(test.grace, test.max)
			if policy.GraceSeconds() != test.wantGrace || policy.MaxAttempts() != test.wantMax {
				t.Fatalf("policy = grace %d, max %d", policy.GraceSeconds(), policy.MaxAttempts())
			}
		})
	}
}

func TestRetryPolicyDecision(t *testing.T) {
	name, err := ParseRecordName("001.msg")
	if err != nil {
		t.Fatal(err)
	}
	other, err := ParseRecordName("002.msg")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	for _, test := range []struct {
		name       string
		grace, max string
		oldest     *OldestRecord
		state      RetryState
		marker     RecordName
		want       RetryAction
		wantCount  uint64
		wantReset  bool
	}{
		{"no oldest", "", "", nil, NewRetryState(name, 2, 900), name, RetryQuiet, 0, true},
		{"below grace", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(911, 0)}, RetryState{}, RecordName{}, RetryQuiet, 0, false},
		{"age boundary", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(910, 0)}, RetryState{}, RecordName{}, RetryRing, 0, false},
		{"spacing before", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, NewRetryState(name, 1, 911), RecordName{}, RetryQuiet, 1, false},
		{"spacing boundary", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, NewRetryState(name, 1, 910), RecordName{}, RetryRing, 1, false},
		{"missing ladder cannot carry count", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, NewRetryState(RecordName{}, 3, 999), RecordName{}, RetryRing, 0, false},
		{"max before spacing", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, NewRetryState(name, 3, 999), RecordName{}, RetryEscalate, 3, false},
		{"exact escalation count", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, NewRetryState(name, 7, 999), RecordName{}, RetryEscalate, 7, false},
		{"max zero", "", "0", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, RetryState{}, RecordName{}, RetryEscalate, 0, false},
		{"zero grace", "0", "1", &OldestRecord{Name: name, CreatedAt: now}, RetryState{}, RecordName{}, RetryRing, 0, false},
		{"matching marker", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, NewRetryState(name, 3, 800), name, RetryQuiet, 3, false},
		{"different marker", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, RetryState{}, other, RetryRing, 0, false},
		{"changed oldest", "", "", &OldestRecord{Name: other, CreatedAt: time.Unix(800, 0)}, NewRetryState(name, 3, 999), name, RetryRing, 0, true},
		{"future last", "", "", &OldestRecord{Name: name, CreatedAt: time.Unix(800, 0)}, NewRetryState(name, 1, 1100), RecordName{}, RetryQuiet, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := NewRetryPolicy(
				test.grace,
				test.max,
			).Decide(now, test.oldest, test.state, test.marker)
			if decision.Action() != test.want ||
				decision.NextState().Attempts() != test.wantCount ||
				decision.ClearBookkeeping() != test.wantReset {
				t.Fatalf(
					"decision = %v, count %d, reset %v",
					decision.Action(),
					decision.NextState().Attempts(),
					decision.ClearBookkeeping(),
				)
			}
			wantRecord := RecordName{}
			if test.oldest != nil {
				wantRecord = test.oldest.Name
			}
			if decision.RecordName() != wantRecord {
				t.Fatalf("decision record = %q, want %q", decision.RecordName(), wantRecord)
			}
			wantStateRecord := test.state.RecordName()
			if test.wantReset {
				wantStateRecord = wantRecord
			}
			if decision.NextState().RecordName() != wantStateRecord {
				t.Fatalf(
					"next state record = %q, want %q",
					decision.NextState().RecordName(),
					wantStateRecord,
				)
			}
			if test.want == RetryEscalate && decision.EscalationCount() != test.wantCount {
				t.Fatalf("escalation count = %d", decision.EscalationCount())
			}
		})
	}
}

func TestParseRecordName(t *testing.T) {
	for _, value := range []string{"", "foo.msg", "001.txt", "1/2.msg", "001.msg\n"} {
		if _, err := ParseRecordName(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if _, err := ParseRecordName("001.msg"); err != nil {
		t.Fatal(err)
	}
}
