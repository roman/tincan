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
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Task            string `json:"task"`
	SpawnGeneration string `json:"spawn_generation"`
	Body            string `json:"body"`
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
	return assignment, nil
}

func ValidateBoundAssignment(
	store *inbox.Store, taskRoot, taskID, generation string, id inbox.MessageID,
) error {
	worker, _ := inbox.ParseRecipient(taskID)
	assignment, err := store.Read(worker, id)
	if errors.Is(err, os.ErrNotExist) {
		assignment, err = store.ReadHandled(worker, id)
	}
	if err != nil {
		return err
	}
	envelope, err := DecodeAssignment(assignment.Body, taskRoot, taskID)
	if err != nil {
		return err
	}
	if envelope.SpawnGeneration != generation {
		return errors.New("assignment does not match this task and generation")
	}
	return nil
}
