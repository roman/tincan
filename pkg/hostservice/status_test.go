package hostservice

import (
	"context"
	"os"
	"testing"

	"github.com/roman/tincan/pkg/primary"
)

func TestStatusDoesNotRequirePrimaryOwnership(t *testing.T) {
	server, _, socket := fixture(t, func(server *Server) {
		server.authorize = func(primary.Caller) error {
			t.Error("read-only status reached primary authorization")
			return primary.ErrUnauthorizedCaller
		}
	})
	response, err := Call(
		context.Background(),
		socket,
		Request{Operation: HostStatus, Status: &StatusRequest{}},
	)
	if err != nil || response.Outcome != Completed || response.Status == nil {
		t.Fatalf("status: %+v, %v", response, err)
	}
	if response.Status.Root != server.config.Root ||
		response.Status.MessageRoot != server.config.MessageRoot ||
		response.Status.Recipient != server.config.Recipient.String() ||
		response.Status.Executable != server.config.WorkerCommands.Concierge {
		t.Fatalf("wrong host identity: %+v", response.Status)
	}
	tasks, err := server.store.List(context.Background())
	if err != nil || len(tasks) != 0 {
		t.Fatal("status changed task state")
	}
}

func TestStatusRequiresHostUID(t *testing.T) {
	if err := authorizeStatusCaller(primary.Caller{UID: uint32(os.Geteuid())}); err != nil {
		t.Fatal(err)
	}
	if err := authorizeStatusCaller(primary.Caller{UID: uint32(os.Geteuid()) + 1}); err == nil {
		t.Fatal("status admitted another user")
	}
}

func TestStatusHandlerRejectsForeignUIDBeforeComparingConfiguration(t *testing.T) {
	server, _, _ := fixture(t, nil)
	caller := primary.Caller{UID: uint32(os.Geteuid()) + 1}
	for _, messageRoot := range []string{server.config.MessageRoot, "/different/messages"} {
		response := server.statusResponse(
			caller,
			Request{
				Operation:    HostStatus,
				Status:       &StatusRequest{},
				Expectations: &Expectations{MessageRoot: messageRoot},
			},
		)
		if response.Outcome != Unauthorized || response.Status != nil {
			t.Fatalf("foreign caller learned configuration match: %+v", response)
		}
	}
}

func TestStatusRejectsMixedRequestsAndContradictoryResponses(t *testing.T) {
	valid := Request{Operation: HostStatus, Status: &StatusRequest{}}
	if err := validateRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, request := range []Request{
		{Operation: HostStatus},
		{Operation: HostStatus, Status: &StatusRequest{}, Intake: intakeRequest().Intake},
		{Operation: Intake, Status: &StatusRequest{}},
	} {
		if err := validateRequest(request); err == nil {
			t.Fatal("invalid status request accepted")
		}
	}
	status := &StatusResult{
		Root:        "/state",
		MessageRoot: "/messages",
		Recipient:   "concierge",
		Executable:  "/tools/concierge",
		Session:     "work",
	}
	if err := validateResponse(Response{Outcome: Completed, Status: status}, valid); err != nil {
		t.Fatal(err)
	}
	for _, response := range []Response{
		{Outcome: Completed},
		{Outcome: Completed, Status: status, Task: &TaskResult{}},
		{Outcome: Failed, Error: "failed", Status: status},
		{Outcome: Uncertain, Error: "uncertain", Status: status},
	} {
		if err := validateResponse(response, valid); err == nil {
			t.Fatal("invalid status response accepted")
		}
	}
	if err := validateResponse(Response{Outcome: Completed, Status: status}, intakeRequest()); err == nil {
		t.Fatal("status data accepted for mutation")
	}
}
