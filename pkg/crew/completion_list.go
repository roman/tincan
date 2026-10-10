package crew

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/roman/tincan/pkg/firstmate"
)

// PendingCompletion identifies a backlog transition that still needs finishing.
type PendingCompletion struct {
	TaskID string
	Mode   CompletionMode
}

// ListPendingCompletions reads durable completion markers, including tasks whose
// metadata and crew records have already been retired.
func (registry *Registry) ListPendingCompletions() ([]PendingCompletion, error) {
	if registry == nil || registry.root == "" {
		return nil, ErrInvalidRecord
	}
	root, err := verifiedRoot(registry.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	taskRoot, selected, err := openSelectedDirectory(root, "tasks")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer taskRoot.Close()
	directory, err := taskRoot.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var pending []PendingCompletion
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".completion.json") {
			continue
		}
		completion, found, err := pendingCompletion(taskRoot, entry.Name())
		if err != nil {
			return nil, err
		}
		if found {
			pending = append(pending, completion)
		}
	}
	sort.Slice(pending, func(left, right int) bool {
		return pending[left].TaskID < pending[right].TaskID
	})
	if !sameDirectory(selected, filepath.Join(registry.root, "tasks")) {
		return nil, ErrStaleRecord
	}
	return pending, nil
}

func pendingCompletion(taskRoot *os.Root, name string) (PendingCompletion, bool, error) {
	id, err := firstmate.ParseTaskID(strings.TrimSuffix(name, ".completion.json"))
	if err != nil {
		return PendingCompletion{}, false, fmt.Errorf("completion %s: %w", name, err)
	}
	marker, _, err := readCompletionMarker(taskRoot, id)
	if errors.Is(err, os.ErrNotExist) {
		return PendingCompletion{}, false, nil
	}
	if err != nil {
		return PendingCompletion{}, false, fmt.Errorf("completion %s: %w", id, err)
	}
	return PendingCompletion{TaskID: id.String(), Mode: marker.Mode}, true, nil
}
