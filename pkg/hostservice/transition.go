package hostservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"time"

	"github.com/roman/tincan/pkg/taskstate"
)

const (
	Start           Operation = "start"
	Finish          Operation = "finish"
	Hold            Operation = "hold"
	Release         Operation = "release"
	Cleanup         Operation = "cleanup"
	ReleaseWorktree Operation = "release_worktree"
	Cancel          Operation = "cancel"
	Stop            Operation = "stop"
)

type TransitionRequest struct {
	TaskID     string                   `json:"task_id"`
	Generation string                   `json:"generation,omitempty"`
	Mode       taskstate.CompletionMode `json:"mode,omitempty"`
	ClosedOn   string                   `json:"closed_on,omitempty"`
	Hold       *taskstate.Hold          `json:"hold,omitempty"`
}

func validateRequest(request Request) error {
	payloads := 0
	for _, present := range []bool{request.Intake != nil, request.Transition != nil, request.Resolve != nil, request.Adoption != nil, request.Assignment != nil, request.Control != nil, request.Supervision != nil, request.Launch != nil, request.Status != nil} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return errors.New("host operation requires exactly one payload")
	}
	if request.Operation == HostStatus {
		if request.Status == nil {
			return errors.New("status requires a status payload")
		}
		return nil
	}
	if request.Status != nil {
		return errors.New("unrelated status payload")
	}
	if request.Operation == Launch {
		return validateLaunchInput(request.Launch)
	}
	if request.Launch != nil {
		return errors.New("unrelated launch payload")
	}
	if request.Operation == Supervise || request.Operation == StopSupervision {
		return validateSupervisionInput(request.Operation, request.Supervision)
	}
	if request.Supervision != nil {
		return errors.New("unrelated supervision payload")
	}
	if request.Operation == ControlStart || request.Operation == ControlInterrupt {
		return validateControlInput(request.Operation, request.Control)
	}
	if request.Control != nil {
		return errors.New("unrelated control payload")
	}
	if request.Operation == Adopt {
		return validateAdoptionInput(request.Adoption)
	}
	if request.Operation == SendAssignment {
		return validateAssignmentInput(request.Assignment)
	}
	if request.Adoption != nil || request.Assignment != nil {
		return errors.New("unrelated worker payload")
	}

	if request.Operation == Resolve {
		return validateResolveRequest(request)
	}
	if request.Resolve != nil {
		return errors.New("unrelated resolution payload")
	}
	if request.Operation == Intake {
		if request.Intake == nil || request.Transition != nil {
			return errors.New("intake requires only an intake definition")
		}
		_, err := taskstate.NewQueued(*request.Intake)
		return err
	}
	input := request.Transition
	if input == nil || request.Intake != nil {
		return errors.New("lifecycle operation requires only a transition payload")
	}
	// Validate identifiers and optional hold with the same task contract as intake.
	if _, err := taskstate.NewQueued(taskstate.Definition{ID: input.TaskID, Title: "validation", Kind: "task", Repo: "validation", Hold: input.Hold}); err != nil {
		return err
	}
	switch request.Operation {
	case Start, Release, Cancel:
		if input.Hold != nil || input.Generation != "" || input.Mode != "" || input.ClosedOn != "" {
			return errors.New("start and release accept only a task ID")
		}
	case Hold:
		if input.Hold == nil || input.Generation != "" || input.Mode != "" || input.ClosedOn != "" {
			return errors.New("hold requires only a task ID and hold")
		}
	case Cleanup, Stop, AbandonLaunch, ReleaseWorktree:
		if input.Generation == "" || input.Mode != "" || input.ClosedOn != "" || input.Hold != nil {
			return errors.New("operation requires only a task ID and generation")
		}
		return firstmate.ValidateSpawnGeneration(input.Generation)
	case Finish:
		if input.Hold != nil || input.Generation == "" {
			return errors.New("finish requires a generation and no hold")
		}
		if input.Mode != taskstate.Close && input.Mode != taskstate.Retain {
			return errors.New("finish mode must be close or retain")
		}
		if err := firstmate.ValidateSpawnGeneration(input.Generation); err != nil {
			return err
		}
		if input.Mode == taskstate.Close {
			date, err := time.Parse("2006-01-02", input.ClosedOn)
			if err != nil || date.Format("2006-01-02") != input.ClosedOn {
				return errors.New("close requires a valid closing date")
			}
		} else if input.ClosedOn != "" {
			return errors.New("retain forbids a closing date")
		}

	default:
		return fmt.Errorf("unknown host operation %q", request.Operation)
	}
	return nil
}

func (server *Server) execute(ctx context.Context, request Request) (taskstate.Task, error) {
	if request.Operation == Intake {
		return server.create(ctx, *request.Intake)
	}
	input := request.Transition
	switch request.Operation {
	case Start:
		return server.service.Start(ctx, input.TaskID)
	case Finish:
		return server.service.Finish(
			ctx,
			input.TaskID,
			input.Generation,
			input.Mode,
			input.ClosedOn,
		)
	case Hold:
		return server.service.PlaceHold(ctx, input.TaskID, *input.Hold)
	case Release:
		return server.service.ReleaseHold(ctx, input.TaskID)
	case Cancel:
		return server.service.CancelAdoption(ctx, input.TaskID)
	case ReleaseWorktree:
		return server.service.ReleaseWorktree(ctx, input.TaskID, input.Generation)
	case Cleanup:
		return server.service.Retire(
			ctx,
			input.TaskID,
			input.Generation,
			herdr.NewClient(server.config.HerdrExecutable),
		)
	default:
		return taskstate.Task{}, errors.New("unknown host operation")
	}
}

func validateTransitionResponse(response Response, request Request) error {
	if err := validateRequest(request); err != nil {
		return err
	}
	if response.Transition == nil || response.Task != nil || response.Error != "" {
		return errors.New("host returned an incomplete lifecycle result")
	}
	task, err := taskstate.NewMigrated(*response.Transition)
	if err != nil {
		return err
	}
	input := request.Transition
	if task.ID() != input.TaskID {
		return errors.New("host returned a different task")
	}

	var repeated taskstate.Task
	switch request.Operation {
	case Start:
		repeated, err = task.Start()
	case Hold:
		repeated, err = task.PlaceHold(*input.Hold)
	case Release:
		repeated, err = task.ReleaseHold()
	case Finish:
		repeated, err = task.AcceptResult(input.Generation, input.Mode, input.ClosedOn)
	case ReleaseWorktree:
		lease, found := task.RetiredLease(input.Generation)
		if !found || lease == (taskstate.WorktreeLease{}) ||
			lease.State != taskstate.LeaseReturned {
			return errors.New("host returned an unreleased worktree lease")
		}
		repeated, err = task.RecordLeaseReturned(input.Generation, lease)
	case Cleanup:
		repeated, err = task.RetireResult(input.Generation)
	case Cancel:
		repeated, err = task.CancelAdoption()
	}
	if err != nil {
		return err
	}
	actual, err := task.Encode()
	if err != nil {
		return err
	}
	retried, err := repeated.Encode()
	if err != nil {
		return err
	}
	// A completed response must already satisfy the domain's identical retry.
	if !bytes.Equal(actual, retried) {
		return errors.New("host returned an unapplied lifecycle result")
	}
	return nil
}
