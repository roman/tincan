package hostservice

import (
	"context"
	"errors"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

const Resolve Operation = "resolve"

type ResolveRequest struct {
	ReplyID  string                   `json:"reply_id"`
	Mode     taskstate.CompletionMode `json:"mode"`
	ClosedOn string                   `json:"closed_on,omitempty"`
}

func validateResolveRequest(request Request) error {
	input := request.Resolve
	if input == nil || request.Intake != nil || request.Transition != nil {
		return errors.New("resolve requires only a reply resolution payload")
	}
	if _, err := inbox.ParseMessageID(input.ReplyID); err != nil {
		return err
	}
	if input.Mode == taskstate.Retain {
		if input.ClosedOn != "" {
			return errors.New("retain forbids a closing date")
		}
		return nil
	}
	if input.Mode != taskstate.Close {
		return errors.New("resolution mode must be close or retain")
	}
	date, err := time.Parse("2006-01-02", input.ClosedOn)
	if err != nil || date.Format("2006-01-02") != input.ClosedOn {
		return errors.New("close requires a valid closing date")
	}
	return nil
}

func (server *Server) perform(ctx context.Context, request Request) (Response, error) {
	if request.Operation == Stop || request.Operation == Cleanup {
		return server.withTaskEndpoint(
			ctx,
			request,
			func() (Response, error) { return server.performOperation(ctx, request) },
		)
	}
	return server.performOperation(ctx, request)
}

func (server *Server) performOperation(ctx context.Context, request Request) (Response, error) {
	if request.Operation == Launch {
		return server.launch(ctx, *request.Launch)
	}
	if request.Operation == Supervise || request.Operation == StopSupervision {
		return server.supervision(ctx, request.Operation, *request.Supervision)
	}
	if request.Operation == ControlStart || request.Operation == ControlInterrupt {
		return server.control(ctx, request.Operation, *request.Control)
	}
	if request.Operation == Adopt {
		return server.adopt(ctx, *request.Adoption)
	}
	if request.Operation == SendAssignment {
		return server.sendAssignment(ctx, *request.Assignment)
	}

	if request.Operation == Stop {
		input := request.Transition
		if err := server.service.Stop(ctx, input.TaskID, input.Generation, herdr.NewClient(server.config.HerdrExecutable)); err != nil {
			return Response{}, err
		}
		return Response{
			Stopped: &StopResult{TaskID: input.TaskID, Generation: input.Generation},
		}, nil
	}

	if request.Operation == Resolve {
		input := request.Resolve
		result, err := server.service.Resolve(
			ctx,
			server.config.MessageRoot,
			input.ReplyID,
			input.Mode,
			input.ClosedOn,
		)
		response := Response{}
		if result.TaskID != "" {
			response.Resolution = &result
		}
		return response, err
	}
	task, err := server.execute(ctx, request)
	response := Response{}
	if task.ID() != "" {
		if request.Operation == Intake {
			response.Task = &TaskResult{Definition: task.Definition(), State: task.State()}
		} else {
			snapshot := task.Snapshot()
			response.Transition = &snapshot
		}
	}
	return response, err
}

func validateResolutionResponse(response Response, request Request) error {
	if err := validateResolveRequest(request); err != nil {
		return err
	}
	if response.Resolution == nil || response.Task != nil || response.Transition != nil ||
		response.Error != "" {
		return errors.New("host returned an incomplete resolution result")
	}
	if _, err := firstmate.ParseTaskID(response.Resolution.TaskID); err != nil {
		return err
	}
	expected := taskstate.Done
	if request.Resolve.Mode == taskstate.Retain {
		expected = taskstate.Queued
	}
	if response.Resolution.State != expected {
		return errors.New("host returned a different resolution state")
	}
	return nil
}
