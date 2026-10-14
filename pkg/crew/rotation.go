package crew

import (
	"encoding/json"
	"fmt"
	"github.com/roman/tincan/internal/lockfile"
	"os"
	"reflect"
	"syscall"

	"github.com/roman/tincan/pkg/firstmate"
)

// ReserveLaunch keeps one generation across message publication and launch retries.
func (task *CurrentTask) ReserveLaunch(candidate string) (string, error) {
	if task == nil || task.crewRoot == nil ||
		firstmate.ValidateSpawnGeneration(candidate) != nil {
		return "", ErrInvalidRecord
	}
	var generation string
	err := withCrewLock(task.crewRoot, func() error {
		latest, err := readRecord(task.crewRoot, task.id.String())
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(latest, task.record) {
			return ErrConflict
		}
		if latest.PendingLaunch != "" {
			generation = latest.PendingLaunch
			return nil
		}
		if candidate == latest.SpawnGeneration {
			return ErrInvalidRecord
		}
		latest.PendingLaunch = candidate
		if err := replaceRecord(task.crewRoot, latest); err != nil {
			return err
		}
		task.record = latest
		generation = candidate
		return nil
	})
	return generation, err
}

// CompleteLaunch clears a reserved generation after Herdr confirms the worker.
func (task *CurrentTask) CompleteLaunch() error {
	if task == nil || task.crewRoot == nil || task.record.PendingLaunch == "" ||
		task.record.PendingLaunch != task.record.SpawnGeneration {
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
		latest.PendingLaunch = ""
		if err := replaceRecord(task.crewRoot, latest); err != nil {
			return err
		}
		task.record = latest
		return nil
	})
}

// CancelLaunch releases a future launch when the current worker is alive.
func (task *CurrentTask) CancelLaunch() error {
	if task == nil || task.crewRoot == nil || task.record.PendingLaunch == "" ||
		task.record.PendingLaunch == task.record.SpawnGeneration {
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
		latest.PendingLaunch = ""
		if err := replaceRecord(task.crewRoot, latest); err != nil {
			return err
		}
		task.record = latest
		return nil
	})
}

// AdvanceGeneration records a new incarnation before a replacement worker starts.
func (task *CurrentTask) AdvanceGeneration(next string) error {
	if task == nil || task.taskRoot == nil || task.crewRoot == nil ||
		firstmate.ValidateSpawnGeneration(next) != nil ||
		next == task.record.SpawnGeneration ||
		(task.record.PendingLaunch != "" && next != task.record.PendingLaunch) {
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
	lock, err := lockfile.Open(root, ".lock")
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
