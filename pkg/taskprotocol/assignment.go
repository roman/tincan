// Package taskprotocol validates messages tied to a local task generation.
package taskprotocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
)

var ErrInvalidAssignment = errors.New("message is not an assignment for this task root")

type Assignment struct {
	Kind              string `json:"kind"`
	State             string `json:"state"`
	Task              string `json:"task"`
	SpawnGeneration   string `json:"spawn_generation"`
	Body              string `json:"body"`
	OriginalMessageID string `json:"original_message_id,omitempty"`
	PreviousMessageID string `json:"previous_message_id,omitempty"`
}

func DecodeAssignment(body, taskRoot, taskID string) (Assignment, error) {
	var assignment Assignment
	if err := json.Unmarshal([]byte(body), &assignment); err != nil {
		return Assignment{}, fmt.Errorf("invalid assignment: %w", err)
	}
	if assignment.Kind != "task-assignment" ||
		assignment.State != filepath.Join(taskRoot, "tasks") ||
		assignment.Task != taskID || strings.TrimSpace(assignment.Body) == "" {
		return Assignment{}, ErrInvalidAssignment
	}
	if err := firstmate.ValidateSpawnGeneration(assignment.SpawnGeneration); err != nil {
		return Assignment{}, err
	}
	if (assignment.OriginalMessageID == "") != (assignment.PreviousMessageID == "") {
		return Assignment{}, ErrInvalidAssignment
	}
	for _, value := range []string{assignment.OriginalMessageID, assignment.PreviousMessageID} {
		if value != "" {
			if _, err := inbox.ParseMessageID(value); err != nil {
				return Assignment{}, ErrInvalidAssignment
			}
		}
	}
	return assignment, nil
}

func ValidateBoundAssignment(
	store *inbox.Store, taskRoot, taskID, generation string, id inbox.MessageID,
) error {

	assignment, err := ReadAssignment(store, taskRoot, taskID, id)
	if err != nil {
		return err
	}
	if assignment.SpawnGeneration != generation {
		return errors.New("assignment does not match this task and generation")
	}
	return nil
}

// ReadAssignment validates the complete redelivery chain against retained envelopes.
func ReadAssignment(
	store *inbox.Store,
	taskRoot, taskID string,
	id inbox.MessageID,
) (Assignment, error) {
	recipient, _ := inbox.ParseRecipient(taskID)
	seen := map[string]bool{}
	var result Assignment
	current := id
	generations := map[string]bool{}
	for {
		if seen[current.String()] {
			return Assignment{}, ErrInvalidAssignment
		}
		seen[current.String()] = true
		message, err := store.Read(recipient, current)
		if errors.Is(err, os.ErrNotExist) {
			message, err = store.ReadHandled(recipient, current)
		}
		if err != nil {
			return Assignment{}, err
		}
		assignment, err := DecodeAssignment(message.Body, taskRoot, taskID)
		if err != nil {
			return Assignment{}, err
		}
		if generations[assignment.SpawnGeneration] {
			return Assignment{}, ErrInvalidAssignment
		}
		generations[assignment.SpawnGeneration] = true
		if current == id {
			result = assignment
		} else if assignment.Body != result.Body {
			return Assignment{}, ErrInvalidAssignment
		}
		if assignment.OriginalMessageID == "" {
			if result.OriginalMessageID != "" && result.OriginalMessageID != current.String() {
				return Assignment{}, ErrInvalidAssignment
			}
			return result, nil
		}
		if result.OriginalMessageID != assignment.OriginalMessageID ||
			message.Key != RedeliveryKey(
				taskRoot,
				taskID,
				assignment.SpawnGeneration,
				assignment.OriginalMessageID,
			) {
			return Assignment{}, ErrInvalidAssignment
		}
		previous, _ := inbox.ParseMessageID(assignment.PreviousMessageID)
		if previous == current {
			return Assignment{}, ErrInvalidAssignment
		}
		current = previous
	}
}

// RedeliveryKey identifies one original assignment in one target generation.
func RedeliveryKey(taskRoot, taskID, generation, originalID string) string {
	return fmt.Sprintf(
		"task-assignment-redelivery:%s:%s:%s:%s",
		taskRoot,
		taskID,
		generation,
		originalID,
	)
}
