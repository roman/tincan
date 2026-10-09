package firstmate

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ListTasks reads local task metadata in task ID order without changing state.
func ListTasks(stateDir AbsolutePath) ([]Metadata, error) {
	if stateDir.value == "" {
		return nil, ErrInvalidPath
	}
	selected, err := os.Lstat(stateDir.value)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !selected.IsDir() {
		return nil, fmt.Errorf("%w: task state is not a directory", ErrInvalidMetadata)
	}
	root, err := os.OpenRoot(stateDir.value)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(selected, opened) {
		return nil, fmt.Errorf("%w: task state changed during open", ErrInvalidMetadata)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	tasks := make([]Metadata, 0)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".meta") {
			continue
		}
		id, err := ParseTaskID(strings.TrimSuffix(entry.Name(), ".meta"))
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, entry.Name())
		}
		metadata, err := ReadTaskFromRoot(root, id)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", id.String(), err)
		}
		tasks = append(tasks, metadata)
	}
	sort.Slice(tasks, func(left, right int) bool {
		return tasks[left].TaskID().String() < tasks[right].TaskID().String()
	})
	return tasks, nil
}
