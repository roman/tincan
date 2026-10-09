package crew

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

// CompletionMode selects the backlog state after a worker is retired.
type CompletionMode string

const (
	CloseTask  CompletionMode = "close"
	RetainTask CompletionMode = "retain"
)

type completionMarker struct {
	TaskID          string         `json:"task_id"`
	SpawnGeneration string         `json:"spawn_generation"`
	Session         string         `json:"session"`
	Pane            string         `json:"pane"`
	Mode            CompletionMode `json:"mode"`
}

// Completion keeps one task's records and pending transition under its metadata lock.
type Completion struct {
	id         firstmate.TaskID
	taskRoot   *os.Root
	crewRoot   *os.Root
	check      func() error
	crewPath   string
	crewInfo   os.FileInfo
	marker     completionMarker
	markerInfo os.FileInfo
	metadata   firstmate.Metadata
	metaInfo   os.FileInfo
	record     Record
	recordInfo os.FileInfo
}

func markerName(id firstmate.TaskID) string { return id.String() + ".completion.json" }

func completionPending(root *os.Root, id firstmate.TaskID) (bool, error) {
	_, err := root.Lstat(markerName(id))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func validCompletionMode(mode CompletionMode) bool {
	return mode == CloseTask || mode == RetainTask
}

// WithCompletion holds the task lock while the caller checks and changes its backlog row.
func (registry *Registry) WithCompletion(taskID string, operation func(*Completion) error) error {
	if operation == nil {
		return ErrInvalidRecord
	}
	id, err := firstmate.ParseTaskID(taskID)
	if err != nil {
		return err
	}
	return registry.withLockedTask(taskID, func(taskRoot *os.Root, check func() error) error {
		crewRoot, err := registry.openCrew()
		if err != nil {
			return err
		}
		defer crewRoot.Close()
		crewDirectory, err := crewRoot.Open(".")
		if err != nil {
			return err
		}
		crewInfo, err := crewDirectory.Stat()
		crewDirectory.Close()
		if err != nil {
			return err
		}
		completion := &Completion{
			id: id, taskRoot: taskRoot, crewRoot: crewRoot, check: check,
			crewPath: filepath.Join(registry.root, "crew"), crewInfo: crewInfo,
		}
		if err := completion.checkCurrent(); err != nil {
			return err
		}
		if err := completion.load(); err != nil {
			return err
		}
		return operation(completion)
	})
}

func (completion *Completion) checkCurrent() error {
	if err := completion.check(); err != nil {
		return err
	}
	if !sameDirectory(completion.crewInfo, completion.crewPath) {
		return fmt.Errorf("%w: crew directory changed", ErrStaleRecord)
	}
	if completion.metaInfo != nil {
		metadata, selected, err := firstmate.ReadTaskWithInfoFromRoot(
			completion.taskRoot, completion.id,
		)
		if err != nil || !os.SameFile(selected, completion.metaInfo) ||
			!reflect.DeepEqual(metadata, completion.metadata) {
			return fmt.Errorf("%w: task metadata changed", ErrStaleRecord)
		}
	}
	if completion.recordInfo != nil {
		record, selected, err := readRecordWithInfo(completion.crewRoot, completion.id.String())
		if err != nil || !os.SameFile(selected, completion.recordInfo) ||
			!reflect.DeepEqual(record, completion.record) {
			return fmt.Errorf("%w: crew record changed", ErrStaleRecord)
		}
	}
	if completion.markerInfo != nil {
		marker, selected, err := readCompletionMarker(completion.taskRoot, completion.id)
		if err != nil || !os.SameFile(selected, completion.markerInfo) ||
			marker != completion.marker {
			return fmt.Errorf("%w: completion marker changed", ErrStaleRecord)
		}
	}
	return nil
}

// CheckCurrent confirms the selected task and crew records still name active directories.
func (completion *Completion) CheckCurrent() error {
	if completion == nil {
		return ErrInvalidRecord
	}
	return completion.checkCurrent()
}

func (completion *Completion) load() error {
	marker, info, err := readCompletionMarker(completion.taskRoot, completion.id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	completion.marker, completion.markerInfo = marker, info
	metadata, metaInfo, err := firstmate.ReadTaskWithInfoFromRoot(
		completion.taskRoot, completion.id,
	)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		completion.metadata = metadata
		completion.metaInfo = metaInfo
	}
	record, recordInfo, err := readRecordWithInfo(completion.crewRoot, completion.id.String())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		completion.record = record
		completion.recordInfo = recordInfo
	}
	if info == nil {
		if completion.metaInfo == nil || completion.recordInfo == nil ||
			completion.record.PendingAdoption || completion.record.PendingGeneration != "" ||
			!matchesTask(completion.record, completion.metadata) {
			return ErrStaleRecord
		}
		return nil
	}
	if completion.metaInfo != nil &&
		(completion.metadata.SpawnGeneration() != marker.SpawnGeneration ||
			completion.metadata.HerdrSession() != marker.Session ||
			completion.metadata.HerdrPaneID() != marker.Pane) {
		return ErrStaleRecord
	}
	if completion.recordInfo != nil &&
		(completion.record.SpawnGeneration != marker.SpawnGeneration ||
			completion.record.HerdrSession != marker.Session ||
			completion.record.PaneID != marker.Pane || completion.record.PendingAdoption ||
			completion.record.PendingGeneration != "") {
		return ErrStaleRecord
	}
	if completion.metaInfo != nil && completion.recordInfo != nil &&
		!matchesTask(completion.record, completion.metadata) {
		return ErrStaleRecord
	}
	return nil
}

func readCompletionMarker(
	root *os.Root,
	id firstmate.TaskID,
) (completionMarker, os.FileInfo, error) {
	selected, err := root.Lstat(markerName(id))
	if err != nil {
		return completionMarker{}, nil, err
	}
	if !selected.Mode().IsRegular() {
		return completionMarker{}, nil, ErrInvalidRecord
	}
	file, err := root.Open(markerName(id))
	if err != nil {
		return completionMarker{}, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(selected, opened) {
		return completionMarker{}, nil, ErrStaleRecord
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumRecordBytes+1))
	if err != nil || len(data) > maximumRecordBytes {
		return completionMarker{}, nil, ErrInvalidRecord
	}
	var marker completionMarker
	if json.Unmarshal(data, &marker) != nil || marker.TaskID != id.String() ||
		firstmate.ValidateSpawnGeneration(marker.SpawnGeneration) != nil ||
		!validCompletionMode(marker.Mode) {
		return completionMarker{}, nil, ErrInvalidRecord
	}
	if _, err := herdr.ParseTarget(marker.Session, marker.Pane); err != nil {
		return completionMarker{}, nil, ErrInvalidRecord
	}
	return marker, selected, nil
}

// Pending reports whether an interrupted completion still needs a backlog transition.
func (completion *Completion) Pending() bool { return completion.markerInfo != nil }

// Mode returns the recorded transition for a pending completion.
func (completion *Completion) Mode() CompletionMode { return completion.marker.Mode }

// Retire confirms the exact pane is gone, records intent, then removes both task records.
func (completion *Completion) Retire(
	ctx context.Context, client herdr.Client, mode CompletionMode,
) error {
	if completion == nil || !validCompletionMode(mode) {
		return ErrInvalidRecord
	}
	if completion.Pending() && completion.marker.Mode != mode {
		return ErrConflict
	}
	if err := completion.checkCurrent(); err != nil {
		return err
	}
	if completion.metaInfo != nil || completion.recordInfo != nil {
		session, pane := completion.marker.Session, completion.marker.Pane
		if !completion.Pending() {
			session, pane = completion.record.HerdrSession, completion.record.PaneID
		}
		target, err := herdr.ParseTarget(session, pane)
		if err != nil {
			return err
		}
		if err := client.ConfirmPaneGone(ctx, target); err != nil {
			return err
		}
	}
	if err := completion.checkCurrent(); err != nil {
		return err
	}
	if !completion.Pending() {
		completion.marker = completionMarker{
			TaskID: completion.id.String(), SpawnGeneration: completion.record.SpawnGeneration,
			Session: completion.record.HerdrSession, Pane: completion.record.PaneID, Mode: mode,
		}
		if err := completion.publishMarker(); err != nil {
			return err
		}
	}
	if err := completion.checkCurrent(); err != nil {
		return err
	}
	if completion.metaInfo != nil {
		if err := removeSelected(completion.taskRoot, completion.id.String()+".meta", completion.metaInfo); err != nil {
			return err
		}
		completion.metaInfo = nil
		if err := completion.checkCurrent(); err != nil {
			return err
		}
	}
	if completion.recordInfo != nil {
		if err := withCrewLock(completion.crewRoot, func() error {
			if err := completion.checkCurrent(); err != nil {
				return err
			}
			current, err := readRecord(completion.crewRoot, completion.id.String())
			if err != nil || !reflect.DeepEqual(current, completion.record) {
				return ErrStaleRecord
			}
			return removeSelected(
				completion.crewRoot, completion.id.String()+".json", completion.recordInfo,
			)
		}); err != nil {
			return err
		}
		completion.recordInfo = nil
	}
	return completion.checkCurrent()
}

func (completion *Completion) publishMarker() error {
	data, err := json.Marshal(completion.marker)
	if err != nil {
		return err
	}
	file, name, err := temporaryFile(completion.taskRoot)
	if err != nil {
		return err
	}
	defer completion.taskRoot.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := completion.taskRoot.Link(name, markerName(completion.id)); err != nil {
		return err
	}
	completion.markerInfo = info
	if err := completion.taskRoot.Remove(name); err != nil {
		return err
	}
	return syncRoot(completion.taskRoot)
}

func removeSelected(root *os.Root, name string, selected os.FileInfo) error {
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(selected, current) {
		return fmt.Errorf("%w: %s changed before removal", ErrStaleRecord, name)
	}
	if err := root.Remove(name); err != nil {
		return err
	}
	return syncRoot(root)
}

// Clear removes the pending marker after the backlog transition is confirmed.
func (completion *Completion) Clear() error {
	if completion == nil || !completion.Pending() ||
		completion.metaInfo != nil || completion.recordInfo != nil {
		return ErrConflict
	}
	if err := completion.checkCurrent(); err != nil {
		return err
	}
	if err := removeSelected(
		completion.taskRoot, markerName(completion.id), completion.markerInfo,
	); err != nil {
		return err
	}
	completion.markerInfo = nil
	return completion.checkCurrent()
}
