package hostservice

import (
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskops"
)

func TestAssignmentResponsePreservesConfirmedDelivery(t *testing.T) {
	request := Request{
		Operation:  SendAssignment,
		Assignment: &AssignmentRequest{TaskID: "task-1", Body: "Work"},
	}
	result := AssignmentResult{
		TaskID:            "task-1",
		MessageID:         "k-" + strings.Repeat("a", 64),
		Notification:      taskops.NotificationFailed,
		NotificationError: "worker unavailable",
	}
	for _, outcome := range []Outcome{Completed, Failed, Uncertain} {
		response := Response{Outcome: outcome, Assignment: &result}
		if outcome != Completed {
			response.Error = "later operation failed"
		}
		if err := validateResponse(response, request); err != nil {
			t.Fatalf("confirmed delivery %s: %v", outcome, err)
		}
	}
	if err := validateResponse(Response{Outcome: Uncertain, Error: "publication unconfirmed"}, request); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		alter func(*Response)
	}{
		{"missing result", func(r *Response) { r.Assignment = nil }},
		{"different task", func(r *Response) { r.Assignment.TaskID = "other" }},
		{"invalid message", func(r *Response) { r.Assignment.MessageID = "bad" }},
		{"unknown notification", func(r *Response) { r.Assignment.Notification = "unknown" }},
		{"missing notification error", func(r *Response) { r.Assignment.NotificationError = "" }},
		{"false notification error", func(r *Response) { r.Assignment.Notification = taskops.NotificationSent }},
		{"unrelated payload", func(r *Response) { r.Task = &TaskResult{} }},
		{"unadmitted delivery", func(r *Response) { r.Outcome = Unauthorized; r.Error = "denied" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := result
			response := Response{Outcome: Completed, Assignment: &copy}
			test.alter(&response)
			if err := validateResponse(response, request); err == nil {
				t.Fatal("accepted invalid delivery result")
			}
		})
	}
}

func TestUnknownOutcomeDoesNotSelectRecovery(t *testing.T) {
	if strings.Contains(ErrOutcomeUnknown.Error(), "retry") ||
		strings.Contains(ErrOutcomeUnknown.Error(), "mailbox") ||
		strings.Contains(ErrOutcomeUnknown.Error(), "task") {
		t.Fatalf("unsafe retry guidance: %v", ErrOutcomeUnknown)
	}
}

func TestCompletedResponseRejectsMixedRequestPayloads(t *testing.T) {
	request := Request{
		Operation:  SendAssignment,
		Assignment: &AssignmentRequest{TaskID: "task-1", Body: "Work"},
		Adoption:   &taskops.AdoptionInput{TaskID: "task-1"},
	}
	response := Response{
		Outcome: Completed,
		Assignment: &AssignmentResult{
			TaskID:       "task-1",
			MessageID:    "k-" + strings.Repeat("a", 64),
			Notification: taskops.NotificationSent,
		},
	}
	if err := validateResponse(response, request); err == nil {
		t.Fatal("accepted completed response for mixed request")
	}
}
