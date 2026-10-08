package firstmate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	ErrBlankMessage = errors.New("blank Firstmate message")
	ErrTaskChanged  = errors.New("Firstmate task changed during send")
	ErrNoPrompt     = errors.New("no terminal prompt adapter")
)

// Prompt sends a constant doorbell to a task's terminal endpoint.
type Prompt interface {
	Prompt(context.Context, string, string) error
}

// Sender binds one task's local steering inbox to a terminal prompt adapter.
type Sender struct {
	StateDir AbsolutePath
	TaskID   TaskID
	Actor    LeaseActor
	Prompt   Prompt
	Clock    func() time.Time
}

// Send records an ordinary message before attempting its best-effort doorbell.
// A nonzero RecordPath means the message was committed and must not be retried.
func (sender Sender) Send(ctx context.Context, body string) (result SendResult, err error) {
	if strings.TrimSpace(body) == "" {
		return SendResult{}, ErrBlankMessage
	}
	before, err := ReadTask(sender.StateDir, sender.TaskID)
	if err != nil {
		return SendResult{}, err
	}
	guardErr := WithLeaseGuard(ctx, sender.StateDir, sender.TaskID, sender.Actor, func() error {
		lock, err := acquireSendMetadataLock(ctx, sender.StateDir, sender.TaskID)
		if err != nil {
			return err
		}
		after, err := ReadTask(sender.StateDir, sender.TaskID)
		if err != nil || before != after {
			return errors.Join(ErrTaskChanged, err, lock.Release())
		}
		record, recordErr := sender.writeRecord(ctx, body)
		releaseErr := lock.Release()
		if record.value == "" {
			return errors.Join(recordErr, releaseErr)
		}
		result.RecordPath = record
		result.CleanupErr = errors.Join(recordErr, releaseErr)
		if sender.Prompt == nil {
			result.DoorbellErr = ErrNoPrompt
		} else {
			result.DoorbellErr = sender.Prompt.Prompt(ctx, after.Window(), doorbellText(sender.TaskID))
		}
		return nil
	})
	if result.RecordPath.value != "" {
		result.CleanupErr = errors.Join(result.CleanupErr, guardErr)
		return result, nil
	}
	return result, guardErr
}

// SendResult separates durable recording from advisory terminal delivery.
type SendResult struct {
	RecordPath  AbsolutePath
	DoorbellErr error
	CleanupErr  error
}

const sendLockTimeout = 5 * time.Second

func acquireSendPathLock(ctx context.Context, path string) (*TaskLock, error) {
	bounded, cancel := context.WithTimeout(ctx, sendLockTimeout)
	defer cancel()
	lock, err := acquireLeaseCommandLock(bounded, path)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, ErrLockTimeout
	}
	return lock, err
}

func acquireSendMetadataLock(
	ctx context.Context,
	stateDir AbsolutePath,
	id TaskID,
) (*TaskLock, error) {
	state, err := filepath.EvalSymlinks(stateDir.value)
	if err != nil {
		return nil, err
	}
	return acquireSendPathLock(ctx, filepath.Join(state, ".meta-"+id.value+".lock"))
}

func (sender Sender) writeRecord(ctx context.Context, body string) (AbsolutePath, error) {
	inbox := filepath.Join(sender.StateDir.value, sender.TaskID.value+".inbox")
	if err := os.MkdirAll(filepath.Join(inbox, "handled"), 0700); err != nil {
		return AbsolutePath{}, err
	}
	seqLock, err := acquireSendPathLock(ctx, filepath.Join(inbox, ".seq.lock"))
	if err != nil {
		return AbsolutePath{}, err
	}
	record, writeErr := sender.writeRecordLocked(inbox, body)
	releaseErr := seqLock.Release()
	return record, errors.Join(writeErr, releaseErr)
}

func (sender Sender) writeRecordLocked(inbox, body string) (AbsolutePath, error) {
	sequence, err := nextSequence(inbox)
	if err != nil {
		return AbsolutePath{}, err
	}
	name := fmt.Sprintf("%03d.msg", sequence)
	record, err := ParseAbsolutePath(filepath.Join(inbox, name))
	if err != nil {
		return AbsolutePath{}, err
	}
	staging, err := os.CreateTemp(inbox, ".staging.*")
	if err != nil {
		return AbsolutePath{}, err
	}
	defer os.Remove(staging.Name())
	clock := sender.Clock
	if clock == nil {
		clock = time.Now
	}
	payload := "schema=fm-task-inbox.v1\nat=" + clock().UTC().Format(time.RFC3339) + "\n--\n" + body
	if _, err := staging.WriteString(payload); err != nil {
		_ = staging.Close()
		return AbsolutePath{}, err
	}
	if err := staging.Sync(); err != nil {
		_ = staging.Close()
		return AbsolutePath{}, err
	}
	if err := staging.Close(); err != nil {
		return AbsolutePath{}, err
	}
	if err := os.Rename(staging.Name(), record.value); err != nil {
		return AbsolutePath{}, err
	}
	directory, err := os.Open(inbox)
	if err != nil {
		return record, err
	}
	syncErr := directory.Sync()
	return record, errors.Join(syncErr, directory.Close())
}

func nextSequence(inbox string) (int, error) {
	maximum := 0
	for _, directory := range []string{inbox, filepath.Join(inbox, "handled")} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return 0, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".msg") {
				continue
			}
			digits := strings.TrimSuffix(name, ".msg")
			if digits == "" || strings.Trim(digits, "0123456789") != "" {
				continue
			}
			value, err := strconv.Atoi(digits)
			if err == nil && value > maximum {
				maximum = value
			}
		}
	}
	return maximum + 1, nil
}

func doorbellText(id TaskID) string {
	return ": Firstmate instruction waiting: list \"$FM_TASK_INBOX\"/*.msg in your '" + id.value + ".inbox' steering inbox, read and act on each in numeric order, then mv each into its handled/."
}
