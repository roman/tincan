package firstmate

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidRecordName = errors.New("invalid Firstmate inbox record name")

// RecordName is a validated numeric Firstmate inbox basename.
type RecordName struct{ value string }

// ParseRecordName accepts numeric .msg basenames without path separators.
func ParseRecordName(value string) (RecordName, error) {
	digits, suffix := strings.CutSuffix(value, ".msg")
	if !suffix || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return RecordName{}, ErrInvalidRecordName
	}
	return RecordName{value: value}, nil
}

// String returns the record basename.
func (name RecordName) String() string { return name.value }

// OldestRecord identifies the oldest ordinary pending record and its creation time.
type OldestRecord struct {
	Name      RecordName
	CreatedAt time.Time
}

// RetryState is the prior watcher attempt state for one ordinary record.
type RetryState struct {
	record           RecordName
	attempts         uint64
	lastAttemptEpoch int64
}

// NewRetryState constructs state after parsing the persisted attempt count and epoch.
func NewRetryState(record RecordName, attempts uint64, lastAttemptEpoch int64) RetryState {
	if record.value == "" {
		return RetryState{}
	}
	return RetryState{record: record, attempts: attempts, lastAttemptEpoch: lastAttemptEpoch}
}

// RecordName returns the record whose watcher attempts are counted.
func (state RetryState) RecordName() RecordName { return state.record }

// Attempts returns the number of watcher doorbell attempts already made.
func (state RetryState) Attempts() uint64 { return state.attempts }

// LastAttemptEpoch returns the prior attempt's Unix timestamp.
func (state RetryState) LastAttemptEpoch() int64 { return state.lastAttemptEpoch }

// RetryPolicy holds the parsed grace period and watcher attempt budget.
type RetryPolicy struct {
	graceSeconds uint64
	maxAttempts  uint64
}

// NewRetryPolicy parses the Firstmate environment values, preserving zero.
func NewRetryPolicy(graceValue, maxValue string) RetryPolicy {
	return RetryPolicy{
		graceSeconds: retryNumber(graceValue, 90),
		maxAttempts:  retryNumber(maxValue, 3),
	}
}

func retryNumber(value string, fallback uint64) uint64 {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// GraceSeconds returns the required age and attempt spacing in whole seconds.
func (policy RetryPolicy) GraceSeconds() uint64 { return policy.graceSeconds }

// MaxAttempts returns the watcher doorbell budget before escalation.
func (policy RetryPolicy) MaxAttempts() uint64 { return policy.maxAttempts }

// RetryAction names the next ordinary watcher action.
type RetryAction uint8

const (
	RetryQuiet RetryAction = iota
	RetryRing
	RetryEscalate
)

// RetryDecision describes one pure watcher decision and bookkeeping change.
type RetryDecision struct {
	action           RetryAction
	record           RecordName
	state            RetryState
	clearBookkeeping bool
}

// Action returns the next watcher action.
func (decision RetryDecision) Action() RetryAction { return decision.action }

// RecordName returns the record selected for a ring or escalation.
func (decision RetryDecision) RecordName() RecordName { return decision.record }

// NextState returns the attempt state after an oldest-record reset.
// A ring does not increment attempts; the caller records its outcome separately.
func (decision RetryDecision) NextState() RetryState { return decision.state }

// ClearBookkeeping indicates that an old ladder and marker should be removed.
func (decision RetryDecision) ClearBookkeeping() bool { return decision.clearBookkeeping }

// EscalationCount returns the number of prior watcher attempts being escalated.
func (decision RetryDecision) EscalationCount() uint64 {
	if decision.action != RetryEscalate {
		return 0
	}
	return decision.state.attempts
}

// Decide applies the ordinary Firstmate watcher retry ladder without I/O.
func (policy RetryPolicy) Decide(
	now time.Time,
	oldest *OldestRecord,
	state RetryState,
	escalated RecordName,
) RetryDecision {
	if oldest == nil {
		return RetryDecision{action: RetryQuiet, clearBookkeeping: true}
	}
	decision := RetryDecision{action: RetryQuiet, record: oldest.Name, state: state}
	age := now.Unix() - oldest.CreatedAt.Unix()
	if age < 0 || uint64(age) < policy.graceSeconds {
		return decision
	}
	if state.record.value != "" && state.record != oldest.Name {
		decision.state = RetryState{record: oldest.Name}
		decision.clearBookkeeping = true
	}
	if escalated == oldest.Name {
		return decision
	}
	if decision.state.attempts >= policy.maxAttempts {
		decision.action = RetryEscalate
		return decision
	}
	spacing := now.Unix() - decision.state.lastAttemptEpoch
	if spacing < 0 || uint64(spacing) < policy.graceSeconds {
		return decision
	}
	decision.action = RetryRing
	return decision
}
