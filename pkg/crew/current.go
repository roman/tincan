package crew

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
)

var ErrStaleRecord = errors.New("crew record does not match current task")

// WithCurrentTask holds the task metadata lock while a caller uses its crew record.
func (registry *Registry) WithCurrentTask(
	taskID string,
	operation func(Record) error,
) (result error) {
	if registry == nil || registry.root == "" || operation == nil {
		return ErrInvalidRecord
	}
	id, err := firstmate.ParseTaskID(taskID)
	if err != nil {
		return err
	}
	state, err := firstmate.ParseAbsolutePath(filepath.Join(registry.root, "tasks"))
	if err != nil {
		return err
	}
	registryRoot, err := verifiedRoot(registry.root)
	if err != nil {
		return err
	}
	defer registryRoot.Close()
	taskRoot, taskInfo, err := openSelectedDirectory(registryRoot, "tasks")
	if err != nil {
		return err
	}
	defer taskRoot.Close()
	crewRoot, crewInfo, err := openSelectedDirectory(registryRoot, "crew")
	if err != nil {
		return err
	}
	defer crewRoot.Close()
	lock, err := firstmate.AcquireTaskLock(state, id, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Release()) }()
	if !sameDirectory(taskInfo, state.String()) ||
		!sameDirectory(crewInfo, filepath.Join(registry.root, "crew")) {
		return fmt.Errorf("%w: task or crew directory changed", ErrStaleRecord)
	}
	metadata, err := firstmate.ReadTaskFromRoot(taskRoot, id)
	if err != nil {
		return err
	}
	record, err := readRecord(crewRoot, taskID)
	if err != nil {
		return err
	}
	if record.Project != metadata.Project().String() ||
		record.Worktree != metadata.Worktree().String() ||
		record.Kind != metadata.Harness() ||
		record.HerdrSession != metadata.HerdrSession() ||
		record.PaneID != metadata.HerdrPaneID() ||
		record.SpawnGeneration != metadata.SpawnGeneration() {
		return fmt.Errorf("%w: %s", ErrStaleRecord, taskID)
	}
	return operation(record)
}

func openSelectedDirectory(root *os.Root, name string) (*os.Root, os.FileInfo, error) {
	selected, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !selected.IsDir() {
		return nil, nil, ErrInvalidRecord
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := child.Open(".")
	if err != nil {
		child.Close()
		return nil, nil, err
	}
	openedInfo, statErr := opened.Stat()
	opened.Close()
	if statErr != nil || !os.SameFile(selected, openedInfo) {
		child.Close()
		return nil, nil, ErrInvalidRecord
	}
	return child, openedInfo, nil
}

func sameDirectory(selected os.FileInfo, path string) bool {
	current, err := os.Stat(path)
	return err == nil && os.SameFile(selected, current)
}
