package taskprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
)

var ErrInvalidReply = errors.New("message is not a task reply for this state root")

type Reply struct {
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Task            string `json:"task"`
	SpawnGeneration string `json:"spawn_generation"`
	AssignmentID    string `json:"assignment_id,omitempty"`
	ReplyKey        string `json:"reply_key,omitempty"`
	Body            string `json:"body"`
}

func DecodeReply(body, messageKey, taskRoot string) (Reply, error) {
	var reply Reply
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		return Reply{}, fmt.Errorf("invalid task reply: %w", err)
	}
	if reply.Kind != "task-reply" || reply.State != filepath.Join(taskRoot, "tasks") ||
		strings.TrimSpace(reply.Body) == "" {
		return Reply{}, ErrInvalidReply
	}
	if _, err := firstmate.ParseTaskID(reply.Task); err != nil {
		return Reply{}, err
	}
	if err := firstmate.ValidateSpawnGeneration(reply.SpawnGeneration); err != nil {
		return Reply{}, err
	}
	if reply.AssignmentID != "" {
		if _, err := inbox.ParseMessageID(reply.AssignmentID); err != nil {
			return Reply{}, err
		}
		expectedKey := fmt.Sprintf("task-reply:%s:%s:%s:%s",
			taskRoot, reply.Task, reply.SpawnGeneration, reply.AssignmentID)
		if strings.TrimSpace(reply.ReplyKey) == "" || messageKey != expectedKey {
			return Reply{}, fmt.Errorf("%w: bound reply has an invalid key", ErrInvalidReply)
		}
	}
	return reply, nil
}
