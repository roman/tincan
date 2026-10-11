package crew

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/presentation"
)

var ErrStaleRecord = errors.New("crew record does not match current task")

// CurrentTask keeps one validated worker binding under its task metadata lock.
type CurrentTask struct {
	record     Record
	metadata   firstmate.Metadata
	id         firstmate.TaskID
	taskRoot   *os.Root
	crewRoot   *os.Root
	taskPath   string
	crewPath   string
	taskInfo   os.FileInfo
	crewInfo   os.FileInfo
	metaInfo   os.FileInfo
	recordInfo os.FileInfo
}

// Record returns the current launch details.
func (task *CurrentTask) Record() Record {
	record := task.record
	record.AgentArgs = append([]string(nil), record.AgentArgs...)
	return record
}

// CheckCurrent rejects task or crew replacement after a blocking operation.
func (task *CurrentTask) CheckCurrent() error {
	if task == nil || !sameDirectory(task.taskInfo, task.taskPath) ||
		!sameDirectory(task.crewInfo, task.crewPath) {
		return ErrStaleRecord
	}
	metadata, metaInfo, err := firstmate.ReadTaskWithInfoFromRoot(task.taskRoot, task.id)
	if err != nil || !os.SameFile(metaInfo, task.metaInfo) ||
		!reflect.DeepEqual(metadata, task.metadata) {
		return ErrStaleRecord
	}
	record, recordInfo, err := readRecordWithInfo(task.crewRoot, task.id.String())
	if err != nil || !os.SameFile(recordInfo, task.recordInfo) ||
		!reflect.DeepEqual(record, task.record) {
		return ErrStaleRecord
	}
	return nil
}

// StopPane closes the worker pane while retaining the task records for finish.
func (task *CurrentTask) StopPane(ctx context.Context, client herdr.Client) error {
	target, err := herdr.ParseTarget(task.record.HerdrSession, task.record.PaneID)
	if err != nil {
		return err
	}
	return presentation.CloseBoundPane(ctx, client, target,
		task.metadata.HerdrWorkspaceID(), task.metadata.HerdrTabID(), task.CheckCurrent)
}

// WithCurrentTask holds the task metadata lock while a caller uses its crew record.
func (registry *Registry) WithCurrentTask(
	taskID string, operation func(*CurrentTask) error,
) error {
	return registry.withCurrentTask(taskID, operation, ordinaryTaskAccess)
}

// WithLaunchTask permits a supervisor to recover a reserved worker launch.
func (registry *Registry) WithLaunchTask(
	taskID string, operation func(*CurrentTask) error,
) error {
	return registry.withCurrentTask(taskID, operation, launchTaskAccess)
}

type taskAccess int

const (
	ordinaryTaskAccess taskAccess = iota
	launchTaskAccess
)

func (registry *Registry) withCurrentTask(
	taskID string,
	operation func(*CurrentTask) error,
	access taskAccess,
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
	if pending, err := completionPending(taskRoot, id); err != nil {
		return err
	} else if pending {
		return fmt.Errorf("%w: task completion is pending", ErrStaleRecord)
	}
	record, err := readRecord(crewRoot, taskID)
	if err != nil {
		return err
	}
	if record.PendingAdoption {
		return fmt.Errorf("%w: task adoption is pending", ErrStaleRecord)
	}
	if record.PendingLaunch != "" && access != launchTaskAccess {
		return fmt.Errorf("%w: worker launch is pending", ErrStaleRecord)
	}
	if record.PendingGeneration != "" {
		record, err = recoverPendingGeneration(taskRoot, crewRoot, id, record)
		if err != nil {
			return err
		}
	}
	metadata, metaInfo, err := firstmate.ReadTaskWithInfoFromRoot(taskRoot, id)
	if err != nil {
		return err
	}
	record, recordInfo, err := readRecordWithInfo(crewRoot, taskID)
	if err != nil {
		return err
	}
	if !matchesTask(record, metadata) {
		return fmt.Errorf("%w: %s", ErrStaleRecord, taskID)
	}
	return operation(&CurrentTask{
		record: record, metadata: metadata, id: id, taskRoot: taskRoot, crewRoot: crewRoot,
		taskPath: state.String(), crewPath: filepath.Join(registry.root, "crew"),
		taskInfo: taskInfo, crewInfo: crewInfo, metaInfo: metaInfo, recordInfo: recordInfo,
	})
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

// withLockedTask pins the selected tasks directory while adopting a worker.
func (registry *Registry) withLockedTask(
	taskID string,
	operation func(*os.Root, func() error) error,
) (result error) {
	if registry == nil || registry.root == "" || operation == nil {
		return ErrInvalidRecord
	}
	id, err := firstmate.ParseTaskID(taskID)
	if err != nil {
		return err
	}
	registryRoot, err := verifiedRoot(registry.root)
	if err != nil {
		return err
	}
	defer registryRoot.Close()
	taskRoot, selected, err := openSelectedDirectory(registryRoot, "tasks")
	if err != nil {
		return err
	}
	defer taskRoot.Close()
	path := filepath.Join(registry.root, "tasks")
	state, err := firstmate.ParseAbsolutePath(path)
	if err != nil {
		return err
	}
	lock, err := firstmate.AcquireTaskLock(state, id, 2*time.Second)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Release()) }()
	check := func() error {
		if !sameDirectory(selected, path) {
			return fmt.Errorf("%w: tasks directory changed", ErrStaleRecord)
		}
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	return operation(taskRoot, check)
}
