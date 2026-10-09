package crew

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"syscall"

	"github.com/roman/tincan/pkg/firstmate"
)

// AdvanceGeneration records a new incarnation before a replacement worker starts.
func (task *CurrentTask) AdvanceGeneration(next string) error {
	if task == nil || task.taskRoot == nil || task.crewRoot == nil ||
		firstmate.ValidateSpawnGeneration(next) != nil ||
		next == task.record.SpawnGeneration {
		return ErrInvalidRecord
	}
	return withCrewLock(task.crewRoot, func() error {
		latest, err := readRecord(task.crewRoot, task.id.String())
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(latest, task.record) {
			return ErrConflict
		}
		metadata, err := firstmate.ReadTaskFromRoot(task.taskRoot, task.id)
		if err != nil {
			return err
		}
		if !matchesTask(latest, metadata) {
			return ErrStaleRecord
		}
		pending := latest
		pending.PendingGeneration = next
		if err := replaceRecord(task.crewRoot, pending); err != nil {
			return err
		}
		final, err := completePendingGeneration(task.taskRoot, task.crewRoot, task.id, pending)
		if err == nil {
			task.record = final
		}
		return err
	})
}

func recoverPendingGeneration(
	taskRoot, crewRoot *os.Root,
	id firstmate.TaskID,
	selected Record,
) (Record, error) {
	var recovered Record
	err := withCrewLock(crewRoot, func() error {
		latest, err := readRecord(crewRoot, id.String())
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(latest, selected) {
			return ErrConflict
		}
		recovered, err = completePendingGeneration(taskRoot, crewRoot, id, latest)
		return err
	})
	return recovered, err
}

func completePendingGeneration(
	taskRoot, crewRoot *os.Root,
	id firstmate.TaskID,
	pending Record,
) (Record, error) {
	metadata, err := firstmate.ReadTaskFromRoot(taskRoot, id)
	if err != nil {
		return Record{}, err
	}
	if !sameTaskIdentity(pending, metadata) {
		return Record{}, ErrStaleRecord
	}
	switch metadata.SpawnGeneration() {
	case pending.SpawnGeneration:
		if _, err := firstmate.ReplaceSpawnGenerationLocked(
			taskRoot,
			id,
			pending.SpawnGeneration,
			pending.PendingGeneration,
		); err != nil {
			return Record{}, err
		}
	case pending.PendingGeneration:
	default:
		return Record{}, ErrStaleRecord
	}
	final := pending
	final.SpawnGeneration = pending.PendingGeneration
	final.PendingGeneration = ""
	if err := replaceRecord(crewRoot, final); err != nil {
		return Record{}, err
	}
	return final, nil
}

func withCrewLock(root *os.Root, operation func() error) error {
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := acquireLock(lock); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}

func replaceRecord(root *os.Root, record Record) error {
	if !validRecord(record) {
		return ErrInvalidRecord
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maximumRecordBytes {
		return fmt.Errorf("%w: record exceeds 1 MiB", ErrInvalidRecord)
	}
	temporary, name, err := temporaryFile(root)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := root.Rename(name, record.TaskID+".json"); err != nil {
		return err
	}
	return syncRoot(root)
}
