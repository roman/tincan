package hostservice

import "testing"

func TestStopResponseMatchesRequest(t *testing.T) {
	request := Request{
		Operation:  Stop,
		Transition: &TransitionRequest{TaskID: "task-1", Generation: "gen-1"},
	}
	valid := Response{
		Outcome: Completed,
		Stopped: &StopResult{TaskID: "task-1", Generation: "gen-1"},
	}
	if err := validateResponse(valid, request); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		response Response
	}{
		{"missing", Response{Outcome: Completed}},
		{"wrong task", Response{Outcome: Completed, Stopped: &StopResult{TaskID: "other", Generation: "gen-1"}}},
		{"wrong generation", Response{Outcome: Completed, Stopped: &StopResult{TaskID: "task-1", Generation: "other"}}},
		{"unrelated payload", Response{Outcome: Completed, Stopped: valid.Stopped, Task: &TaskResult{}}},
		{"failed data", Response{Outcome: Failed, Error: "failed", Stopped: valid.Stopped}},
		{"uncertain data", Response{Outcome: Uncertain, Error: "uncertain", Stopped: valid.Stopped}},
		{"unadmitted data", Response{Outcome: Unauthorized, Error: "denied", Stopped: valid.Stopped}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateResponse(test.response, request); err == nil {
				t.Fatal("accepted invalid stop response")
			}
		})
	}
	validRequest := intakeRequest()
	if err := validateResponse(valid, validRequest); err == nil {
		t.Fatal("accepted stop result for intake")
	}
}
