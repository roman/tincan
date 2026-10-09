package firstmate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/roman/tincan/pkg/herdr"
)

// RetryHealth reads a specific terminal endpoint before an ordinary retry action.
type RetryHealth interface {
	Health(context.Context, herdr.Target) herdr.HealthStatus
}

// CheckOutcome describes the one-shot watcher action.
type CheckOutcome uint8

const (
	CheckQuiet CheckOutcome = iota
	CheckWait
	CheckRung
	CheckEscalate
	CheckUnavailable
)

// Escalation describes the pending record to publish as a stale wake.
type Escalation struct {
	TaskID   TaskID
	Record   RecordName
	Attempts uint64
	Outcome  CheckOutcome
}

// CheckResult reports a one-shot retry action, including a ring delivery failure.
type CheckResult struct {
	Outcome  CheckOutcome
	Record   RecordName
	Attempts uint64
	Delivery herdr.RingResult
}

var (
	ErrRetryStoreMismatch = errors.New("retry store does not match task")
	ErrRingNotAttempted   = errors.New("terminal ring not attempted")
)

// RetryCheck binds a task's retry state, endpoint health, and delivery adapters.
type RetryCheck struct {
	StateDir AbsolutePath
	TaskID   TaskID
	Store    RetryStore
	Health   RetryHealth
	Doorbell Doorbell
	Publish  func(context.Context, Escalation) error
}

// Run checks one due ordinary record and performs at most one retry or escalation.
func (check RetryCheck) Run(ctx context.Context, now time.Time) (CheckResult, error) {
	if check.StateDir.value == "" {
		return CheckResult{}, ErrInvalidPath
	}
	if check.TaskID.value == "" {
		return CheckResult{}, ErrInvalidTaskID
	}
	if check.Store.inbox != filepath.Join(check.StateDir.value, check.TaskID.value+".inbox") {
		return CheckResult{}, ErrRetryStoreMismatch
	}
	decision, err := check.Store.Due(now)
	if err != nil {
		return CheckResult{}, err
	}
	if decision.Action() == RetryQuiet {
		return CheckResult{Outcome: CheckQuiet}, nil
	}
	before, err := ReadTask(check.StateDir, check.TaskID)
	if err != nil {
		return CheckResult{}, err
	}
	target, err := herdr.ParseTarget(before.HerdrSession(), before.HerdrPaneID())
	if err != nil {
		return CheckResult{}, fmt.Errorf("%w: terminal target: %v", ErrInvalidMetadata, err)
	}
	if check.Health == nil {
		return CheckResult{}, errors.New("no retry health adapter")
	}
	record := decision.RecordName()
	pending, err := check.Store.pending(record)
	if err != nil {
		return CheckResult{}, err
	}
	if !pending {
		return check.acknowledged(now)
	}
	status := check.Health.Health(ctx, target)
	if err := check.unchanged(before); err != nil {
		return CheckResult{}, err
	}
	pending, err = check.Store.pending(record)
	if err != nil {
		return CheckResult{}, err
	}
	if !pending {
		return check.acknowledged(now)
	}
	if status.Endpoint == herdr.Dead || status.Endpoint == herdr.Missing {
		return check.escalate(ctx, now, record, 0, CheckUnavailable)
	}
	if status.Endpoint == herdr.Alive && status.Activity == herdr.HealthBusy {
		return CheckResult{Outcome: CheckWait, Record: record}, nil
	}
	if decision.Action() == RetryEscalate {
		return check.escalate(ctx, now, record, decision.EscalationCount(), CheckEscalate)
	}
	if check.Doorbell == nil {
		return CheckResult{}, ErrNoDoorbell
	}
	// A worker can exit between the first health read and terminal submission.
	status = check.Health.Health(ctx, target)
	if err := check.unchanged(before); err != nil {
		return CheckResult{}, err
	}
	if status.Endpoint == herdr.Dead || status.Endpoint == herdr.Missing {
		return check.escalate(ctx, now, record, 0, CheckUnavailable)
	}
	if status.Endpoint == herdr.Alive && status.Activity == herdr.HealthBusy {
		return CheckResult{Outcome: CheckWait, Record: record}, nil
	}
	pending, err = check.Store.pending(record)
	if err != nil {
		return CheckResult{}, err
	}
	if !pending {
		return check.acknowledged(now)
	}
	delivery := check.Doorbell.Ring(ctx, target, doorbellText(check.TaskID))
	if err := check.unchanged(before); err != nil {
		return CheckResult{Delivery: delivery}, err
	}
	if delivery.Outcome == herdr.RingUnavailable {
		result, err := check.escalate(ctx, now, record, 0, CheckUnavailable)
		result.Delivery = delivery
		return result, err
	}
	if delivery.Outcome == herdr.RingNotAttempted {
		return CheckResult{Delivery: delivery}, ErrRingNotAttempted
	}
	if err := check.Store.RecordAttempt(record, now); err != nil {
		return CheckResult{}, err
	}
	pending, err = check.Store.pending(record)
	if err != nil {
		return CheckResult{}, err
	}
	if !pending {
		return check.acknowledged(now)
	}
	return CheckResult{
		Outcome:  CheckRung,
		Record:   record,
		Delivery: delivery,
	}, nil
}

func (check RetryCheck) unchanged(before Metadata) error {
	after, err := ReadTask(check.StateDir, check.TaskID)
	if err != nil || before != after {
		return errors.Join(ErrTaskChanged, err)
	}
	return nil
}

func (check RetryCheck) acknowledged(now time.Time) (CheckResult, error) {
	_, err := check.Store.Due(now)
	return CheckResult{Outcome: CheckQuiet}, err
}

func (check RetryCheck) escalate(
	ctx context.Context,
	now time.Time,
	record RecordName,
	count uint64,
	outcome CheckOutcome,
) (CheckResult, error) {
	pending, err := check.Store.pending(record)
	if err != nil {
		return CheckResult{}, err
	}
	if !pending {
		return check.acknowledged(now)
	}
	result := CheckResult{Outcome: outcome, Record: record, Attempts: count}
	if check.Publish == nil {
		return result, nil
	}
	escalation := Escalation{
		TaskID:   check.TaskID,
		Record:   record,
		Attempts: count,
		Outcome:  outcome,
	}
	err = check.Store.PublishEscalation(
		record,
		func() error { return check.Publish(ctx, escalation) },
	)
	if err != nil {
		return result, err
	}
	pending, err = check.Store.pending(record)
	if err != nil {
		return CheckResult{}, err
	}
	if !pending {
		return check.acknowledged(now)
	}
	return result, nil
}
