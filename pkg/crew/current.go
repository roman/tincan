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

// CurrentTask keeps one validated worker binding under its task metadata lock.
type CurrentTask struct {
	record   Record
	id       firstmate.TaskID
	taskRoot *os.Root
	crewRoot *os.Root
}

// Record returns the current launch details.
func (task *CurrentTask) Record() Record {
	record := task.record
	record.AgentArgs = append([]string(nil), record.AgentArgs...)
	return record
}

// WithCurrentTask holds the task metadata lock while a caller uses its crew record.
func (registry *Registry) WithCurrentTask(
	taskID string,
	operation func(*CurrentTask) error,
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
	record, err := readRecord(crewRoot, taskID)
	if err != nil {
		return err
	}
	if record.PendingGeneration != "" {
		record, err = recoverPendingGeneration(taskRoot, crewRoot, id, record)
		if err != nil {
			return err
		}
	}
	metadata, err := firstmate.ReadTaskFromRoot(taskRoot, id)
	if err != nil {
		return err
	}
	if !matchesTask(record, metadata) {
		return fmt.Errorf("%w: %s", ErrStaleRecord, taskID)
	}
	return operation(&CurrentTask{record: record, id: id, taskRoot: taskRoot, crewRoot: crewRoot})
}

func matchesTask(record Record, metadata firstmate.Metadata) bool {
	return sameTaskIdentity(record, metadata) &&
		record.SpawnGeneration == metadata.SpawnGeneration()
}

func sameTaskIdentity(record Record, metadata firstmate.Metadata) bool {
	return record.Project == metadata.Project().String() &&
		record.Worktree == metadata.Worktree().String() &&
		record.Kind == metadata.Harness() &&
		record.HerdrSession == metadata.HerdrSession() &&
		record.PaneID == metadata.HerdrPaneID()
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
