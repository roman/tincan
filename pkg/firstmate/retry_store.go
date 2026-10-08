package firstmate

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RetryStore reads and writes Firstmate's ordinary inbox retry bookkeeping.
type RetryStore struct {
	inbox  string
	policy RetryPolicy
}

// NewRetryStore binds a validated task to its ordinary retry state.
func NewRetryStore(stateDir AbsolutePath, id TaskID, policy RetryPolicy) (RetryStore, error) {
	if stateDir.value == "" {
		return RetryStore{}, ErrInvalidPath
	}
	if id.value == "" {
		return RetryStore{}, ErrInvalidTaskID
	}
	return RetryStore{inbox: filepath.Join(stateDir.value, id.value+".inbox"), policy: policy}, nil
}

// Due evaluates the oldest ordinary pending record and clears stale bookkeeping.
func (store RetryStore) Due(now time.Time) (RetryDecision, error) {
	if store.inbox == "" {
		return RetryDecision{}, ErrInvalidPath
	}
	oldest, err := store.oldestOrdinary()
	if err != nil {
		return RetryDecision{}, err
	}
	if oldest == nil {
		decision := store.policy.Decide(now, nil, RetryState{}, RecordName{})
		return decision, store.clearBookkeeping()
	}
	state := store.readState()
	marker := store.readMarker()
	decision := store.policy.Decide(now, oldest, state, marker)
	if decision.ClearBookkeeping() {
		if err := store.clearBookkeeping(); err != nil {
			return RetryDecision{}, err
		}
	}
	return decision, nil
}

func (store RetryStore) oldestOrdinary() (*OldestRecord, error) {
	entries, err := os.ReadDir(store.inbox)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var selected *OldestRecord
	var selectedSequence uint64
	for _, entry := range entries {
		name, err := ParseRecordName(entry.Name())
		if err != nil {
			continue
		}
		sequence, err := strconv.ParseUint(strings.TrimSuffix(name.value, ".msg"), 10, 64)
		if err != nil {
			continue
		}
		path := filepath.Join(store.inbox, name.value)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || isFireAndForget(path) {
			continue
		}
		if selected == nil || sequence < selectedSequence {
			selected = &OldestRecord{Name: name, CreatedAt: info.ModTime()}
			selectedSequence = sequence
		}
	}
	return selected, nil
}

func isFireAndForget(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "--" {
			break
		}
		if line == "delivery=fire-and-forget" {
			return true
		}
	}
	return false
}

func (store RetryStore) readState() RetryState {
	data, err := os.ReadFile(filepath.Join(store.inbox, ".ring-state"))
	if err != nil {
		return RetryState{}
	}
	fields := strings.Split(strings.TrimSuffix(string(data), "\n"), "\t")
	if len(fields) != 3 {
		return RetryState{}
	}
	name, err := ParseRecordName(fields[0])
	if err != nil {
		return RetryState{}
	}
	count := parseRetryUint(fields[1])
	epoch := parseRetryInt(fields[2])
	return NewRetryState(name, count, epoch)
}

func parseRetryUint(value string) uint64 {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func parseRetryInt(value string) int64 {
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return 0
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func (store RetryStore) readMarker() RecordName {
	data, err := os.ReadFile(filepath.Join(store.inbox, ".escalated"))
	if err != nil {
		return RecordName{}
	}
	name, err := ParseRecordName(strings.TrimSuffix(string(data), "\n"))
	if err != nil {
		return RecordName{}
	}
	return name
}

func (store RetryStore) clearBookkeeping() error {
	for _, name := range []string{".ring-state", ".escalated"} {
		if err := os.Remove(filepath.Join(store.inbox, name)); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// RecordAttempt counts a completed watcher doorbell attempt for a pending record.
func (store RetryStore) RecordAttempt(record RecordName, now time.Time) error {
	pending, err := store.pending(record)
	if err != nil || !pending {
		return err
	}
	state := store.readState()
	count := uint64(1)
	if state.RecordName() == record {
		count = state.Attempts() + 1
	}
	body := fmt.Sprintf("%s\t%d\t%d\n", record.value, count, now.Unix())
	return store.writeIfPending(record, ".ring-state", []byte(body))
}

// MarkEscalated suppresses a repeated wake for one pending ordinary record.
func (store RetryStore) MarkEscalated(record RecordName) error {
	return store.writeIfPending(record, ".escalated", []byte(record.value+"\n"))
}

// PublishEscalation publishes a wake before writing the suppression marker.
func (store RetryStore) PublishEscalation(record RecordName, publish func() error) error {
	if publish == nil {
		return errors.New("nil escalation publisher")
	}
	pending, err := store.pending(record)
	if err != nil || !pending {
		return err
	}
	if err := publish(); err != nil {
		return err
	}
	return store.MarkEscalated(record)
}

func (store RetryStore) pending(record RecordName) (bool, error) {
	if store.inbox == "" {
		return false, ErrInvalidPath
	}
	if record.value == "" {
		return false, ErrInvalidRecordName
	}
	path := filepath.Join(store.inbox, record.value)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular() && !isFireAndForget(path), nil
}

func (store RetryStore) writeIfPending(record RecordName, name string, body []byte) error {
	pending, err := store.pending(record)
	if err != nil || !pending {
		return err
	}
	if err := atomicRetryWrite(store.inbox, name, body); err != nil {
		pending, checkErr := store.pending(record)
		if checkErr == nil && !pending {
			return nil
		}
		return errors.Join(err, checkErr)
	}
	return nil
}

func atomicRetryWrite(directory, name string, body []byte) error {
	staging, err := os.CreateTemp(directory, ".staging.*")
	if err != nil {
		return err
	}
	defer os.Remove(staging.Name())
	if _, err := staging.Write(body); err != nil {
		_ = staging.Close()
		return err
	}
	if err := staging.Sync(); err != nil {
		_ = staging.Close()
		return err
	}
	if err := staging.Close(); err != nil {
		return err
	}
	if err := os.Rename(staging.Name(), filepath.Join(directory, name)); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
