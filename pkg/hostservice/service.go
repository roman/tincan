// Package hostservice admits primary-owned operations at a host Unix socket.
package hostservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/roman/tincan/pkg/primary"
	"github.com/roman/tincan/pkg/supervise"
	"github.com/roman/tincan/pkg/taskops"
	"github.com/roman/tincan/pkg/taskstate"
	"golang.org/x/sys/unix"
)

const maxRequestBytes = 2 << 20
const maxConnections = 8
const ioTimeout = 5 * time.Second
const operationTimeout = 2 * time.Minute

type Operation string

const Intake Operation = "intake"

// Request never carries caller identity or a selectable state root.
type Request struct {
	Launch       *LaunchRequest         `json:"launch,omitempty"`
	Supervision  *SupervisionRequest    `json:"supervision,omitempty"`
	Control      *ControlRequest        `json:"control,omitempty"`
	Adoption     *taskops.AdoptionInput `json:"adoption,omitempty"`
	Assignment   *AssignmentRequest     `json:"assignment,omitempty"`
	Resolve      *ResolveRequest        `json:"resolve,omitempty"`
	Expectations *Expectations          `json:"expectations,omitempty"`
	Operation    Operation              `json:"operation"`
	Intake       *taskstate.Definition  `json:"intake,omitempty"`
	Transition   *TransitionRequest     `json:"transition,omitempty"`
}

type Outcome string

const (
	Completed    Outcome = "completed"
	Unauthorized Outcome = "unauthorized"
	Invalid      Outcome = "invalid"
	Failed       Outcome = "failed"
	Uncertain    Outcome = "uncertain"
	Busy         Outcome = "busy"
)

type TaskResult struct {
	Definition taskstate.Definition `json:"definition"`
	State      taskstate.State      `json:"state"`
}

type StopResult struct {
	TaskID     string `json:"task_id"`
	Generation string `json:"generation"`
}

type Response struct {
	Supervision *SupervisionResult   `json:"supervision,omitempty"`
	Control     *ControlResult       `json:"control,omitempty"`
	Assignment  *AssignmentResult    `json:"assignment,omitempty"`
	Stopped     *StopResult          `json:"stopped,omitempty"`
	Resolution  *taskops.ReplyResult `json:"resolution,omitempty"`
	Outcome     Outcome              `json:"outcome"`
	Task        *TaskResult          `json:"task,omitempty"`
	Error       string               `json:"error,omitempty"`
	Transition  *taskstate.Snapshot  `json:"transition,omitempty"`
}

// Server binds operations to roots selected by the host process.
type Server struct {
	store               *taskstate.Store
	controllers         *controllerRegistry
	diagnosticsMu       sync.Mutex
	config              Config
	service             *taskops.Service
	authorize           func(primary.Caller) error
	create              func(context.Context, taskstate.Definition) (taskstate.Task, error)
	reserveLaunchIntent func(context.Context, string, taskstate.LaunchIntent) (taskstate.Task, error)
}

func New(config Config) (*Server, error) {
	if config.Supervision == (supervise.Config{}) {
		config.Supervision = defaultSupervision()
	}
	if config.Diagnostics == nil {
		config.Diagnostics = os.Stderr
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	root, recipient := config.Root, config.Recipient
	store, err := taskstate.NewStore(root)
	if err != nil {
		return nil, err
	}
	service, err := taskops.NewWithWorkerCommands(root, recipient, config.WorkerCommands)
	if err != nil {
		return nil, err
	}
	return &Server{
		config:              config,
		store:               store,
		service:             service,
		authorize:           func(caller primary.Caller) error { return primary.AuthorizeCaller(config.Root, caller) },
		create:              service.Create,
		reserveLaunchIntent: store.ReserveInitialLaunch,
	}, nil
}

// Serve owns the listener until cancellation. Every connection admits at most
// one operation; clients must stay connected and must not delegate connections.
func (server *Server) Serve(ctx context.Context, listener *net.UnixListener) error {
	return server.ServeReady(ctx, listener, nil)
}

func (server *Server) ServeReady(
	ctx context.Context,
	listener *net.UnixListener,
	ready func() error,
) (result error) {
	lifetime, cancel := context.WithCancel(ctx)
	ctx = lifetime
	server.controllers = newControllerRegistry(ctx)
	server.controllers.report = server.report

	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	defer listener.Close()
	var running sync.WaitGroup
	defer func() { cancel(); running.Wait(); result = errors.Join(result, server.controllers.close()) }()
	if err := server.restoreTaskControllers(ctx); err != nil {
		return err
	}
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	running.Add(1)
	go func() {
		defer running.Done()
		server.runInitialReconciliation(ctx)
	}()
	slots := make(chan struct{}, maxConnections)
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
			running.Add(1)
			go func() {
				defer running.Done()
				defer func() { <-slots }()
				defer connection.Close()
				stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
				defer stop()
				server.handle(ctx, connection)
			}()
		default:
			_ = connection.Close()
		}
	}
}

func peerCaller(connection *net.UnixConn) (primary.Caller, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return primary.Caller{}, err
	}
	var credentials *unix.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		credentials, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err := errors.Join(err, peerErr); err != nil {
		return primary.Caller{}, err
	}
	return primary.Caller{PID: int(credentials.Pid), UID: credentials.Uid}, nil
}

func decodeBounded(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxRequestBytes {
		return errors.New("host request or response exceeds 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing host request or response data")
	}
	return nil
}

func (server *Server) handle(ctx context.Context, connection *net.UnixConn) {
	caller, err := peerCaller(connection)
	if err != nil {
		server.respond(
			connection,
			Response{Outcome: Unauthorized, Error: "kernel caller identity unavailable"},
		)
		return
	}
	_ = connection.SetReadDeadline(time.Now().Add(ioTimeout))
	var request Request
	if err := decodeBounded(connection, &request); err != nil {
		server.respond(connection, Response{Outcome: Invalid, Error: err.Error()})
		return
	}
	if err := errors.Join(validateRequest(request), server.checkExpectations(request.Expectations), server.checkSupervisionSettings(request.Supervision)); err != nil {
		server.respond(connection, Response{Outcome: Invalid, Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		server.respond(connection, Response{Outcome: Failed, Error: err.Error()})
		return
	}
	if err := server.authorize(caller); err != nil {
		outcome := Failed
		switch {
		case errors.Is(err, primary.ErrClaimBusy):
			outcome = Busy
		case errors.Is(err, primary.ErrUnauthorizedCaller):
			outcome = Unauthorized
		}
		server.respond(connection, Response{Outcome: outcome, Error: err.Error()})
		return
	}
	if err := ctx.Err(); err != nil {
		server.respond(connection, Response{Outcome: Failed, Error: err.Error()})
		return
	}
	response, err := server.perform(ctx, request)
	response.Outcome = Completed
	if err != nil {
		response.Outcome, response.Error = Failed, err.Error()
		if errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
			response.Outcome = Uncertain
		}
	}
	server.respond(connection, response)
}

func (server *Server) respond(connection *net.UnixConn, response Response) {
	_ = connection.SetWriteDeadline(time.Now().Add(ioTimeout))
	_ = json.NewEncoder(connection).Encode(response)
}

var ErrOutcomeUnknown = errors.New("host operation outcome is unknown")

// Call retains the authenticated connection through the response. A transport
// failure after connecting cannot establish whether the operation completed.
func Call(ctx context.Context, socket string, request Request) (Response, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return Response{}, err
	}
	if len(data) > maxRequestBytes {
		return Response{}, errors.New("host request exceeds 2 MiB")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return Response{}, fmt.Errorf("host service unavailable: %w", err)
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	_ = connection.SetDeadline(time.Now().Add(operationTimeout + ioTimeout))
	if _, err := connection.Write(data); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	if err := connection.(*net.UnixConn).CloseWrite(); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	var response Response
	if err := decodeBounded(connection, &response); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	if err := validateResponse(response, request); err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	return response, nil
}

func validateResponse(response Response, request Request) error {
	switch response.Outcome {
	case Completed:
		if request.Operation == Supervise || request.Operation == StopSupervision {
			if err := validateRequest(request); err != nil {
				return err
			}
			return validateSupervisionResponse(response, request)
		}
		if response.Supervision != nil {
			return errors.New("host returned an unrelated supervision result")
		}
		if err := validateRequest(request); err != nil {
			return err
		}
		if request.Operation == ControlStart || request.Operation == ControlInterrupt {
			return validateControlResponse(response, request)
		}
		if response.Control != nil {
			return errors.New("host returned an unrelated control result")
		}
		if request.Operation == Adopt {
			return validateAdoptionResponse(response, request)
		}
		if request.Operation == Launch {
			return validateLaunchResponse(response, request)
		}
		if request.Operation == SendAssignment {
			return validateAssignmentResponse(response, request)
		}
		if response.Assignment != nil {
			return errors.New("host returned an unrelated assignment result")
		}
		if request.Operation == Stop {
			if response.Stopped == nil || response.Stopped.TaskID != request.Transition.TaskID ||
				response.Stopped.Generation != request.Transition.Generation ||
				response.Error != "" ||
				response.Task != nil ||
				response.Transition != nil ||
				response.Resolution != nil {
				return errors.New("host returned an incomplete or mismatched stop result")
			}
			return nil
		}
		if response.Stopped != nil {
			return errors.New("host returned an unrelated stop result")
		}
		if request.Operation == Resolve {
			return validateResolutionResponse(response, request)
		}
		if response.Resolution != nil {
			return errors.New("host returned an unrelated resolution result")
		}
		if request.Operation != Intake {
			return validateTransitionResponse(response, request)
		}
		if response.Transition != nil || response.Task == nil || request.Intake == nil ||
			response.Error != "" ||
			response.Task.State != taskstate.Queued {
			return errors.New("host returned an incomplete intake result")
		}
		actual, err := taskstate.NewQueued(response.Task.Definition)
		if err != nil {
			return err
		}
		expected, err := taskstate.NewQueued(*request.Intake)
		if err != nil {
			return err
		}
		actualData, err := actual.Encode()
		if err != nil {
			return err
		}
		expectedData, err := expected.Encode()
		if err != nil {
			return err
		}
		if !bytes.Equal(actualData, expectedData) {
			return errors.New("host returned a different intake definition")
		}
		return nil
	case Failed, Unauthorized, Invalid, Uncertain, Busy:
		if response.Supervision != nil {
			return errors.New("host returned supervision success data for a failed operation")
		}
		if response.Control != nil {
			return errors.New("host returned control success data for a failed operation")
		}
		if request.Operation == Adopt && response.Transition != nil {
			return errors.New("host returned adoption success data for a failed operation")
		}
		if request.Operation == Launch && response.Transition != nil {
			return errors.New("host returned launch success data for a failed operation")
		}
		if response.Stopped != nil {
			return errors.New("host returned stop success data for a failed operation")
		}
		if response.Error == "" {
			return errors.New("host returned an incomplete error result")
		}
		if response.Task != nil || response.Transition != nil || response.Resolution != nil ||
			response.Stopped != nil ||
			response.Assignment != nil {
			if response.Outcome != Failed && response.Outcome != Uncertain {
				return errors.New("host returned data for an unadmitted operation")
			}
			partial := response
			partial.Outcome, partial.Error = Completed, ""
			return validateResponse(partial, request)
		}
		return nil
	default:
		return errors.New("host returned an unknown outcome")
	}
}

func (server *Server) report(err error) {
	server.diagnosticsMu.Lock()
	defer server.diagnosticsMu.Unlock()
	_, _ = fmt.Fprintf(server.config.Diagnostics, "controller_error: %q\n", err.Error())
}
