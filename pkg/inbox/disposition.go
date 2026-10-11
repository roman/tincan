package inbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var ErrDispositionConflict = errors.New("disposition conflicts with an earlier handling attempt")

type DispositionState string

const (
	DispositionStarted  DispositionState = "started"
	DispositionComplete DispositionState = "complete"
)

// Disposition records a handling intent before its side effect and its outcome afterward.
type Disposition struct {
	MessageID MessageID        `json:"message_id"`
	Recipient Recipient        `json:"recipient"`
	Action    string           `json:"action"`
	State     DispositionState `json:"state"`
	Outcome   string           `json:"outcome,omitempty"`
}

func dispositionPath(mailbox mailboxPath, id MessageID) string {
	return filepath.Join(mailbox.String(), "dispositions", id.String()+".json")
}

func readDisposition(mailbox mailboxPath, recipient Recipient, id MessageID) (Disposition, error) {
	var disposition Disposition
	data, err := os.ReadFile(dispositionPath(mailbox, id))
	if err != nil {
		return disposition, err
	}
	if err := json.Unmarshal(data, &disposition); err != nil {
		return Disposition{}, err
	}
	if disposition.MessageID != id || disposition.Recipient != recipient ||
		strings.TrimSpace(disposition.Action) == "" ||
		(disposition.State != DispositionStarted && disposition.State != DispositionComplete) ||
		(disposition.State == DispositionStarted && disposition.Outcome != "") ||
		(disposition.State == DispositionComplete && strings.TrimSpace(disposition.Outcome) == "") {
		return Disposition{}, ErrCorruptMessage
	}
	return disposition, nil
}

func writeDisposition(mailbox mailboxPath, disposition Disposition) error {
	directory := messageDirectoryPath{value: filepath.Join(mailbox.String(), "dispositions")}
	data, err := json.Marshal(disposition)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory.String(), ".writing-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
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
	if err := os.Rename(temporary.Name(), dispositionPath(mailbox, disposition.MessageID)); err != nil {
		return err
	}
	return syncDirectory(directory)
}

// ReadDisposition returns the durable state of one handling attempt.
func (store *Store) ReadDisposition(recipient Recipient, id MessageID) (Disposition, error) {
	if store == nil || store.root == (rootPath{}) {
		return Disposition{}, ErrInvalidRoot
	}
	if !recipient.valid() {
		return Disposition{}, ErrInvalidRecipient
	}
	if !id.valid() {
		return Disposition{}, ErrInvalidID
	}
	return readDisposition(store.mailboxPath(recipient), recipient, id)
}

// BeginDisposition records intent before a caller performs an action.
// A repeated call returns the existing state so callers can inspect a crash boundary.
func (store *Store) BeginDisposition(
	recipient Recipient, id MessageID, action string,
) (Disposition, bool, error) {
	if store == nil || store.root == (rootPath{}) {
		return Disposition{}, false, ErrInvalidRoot
	}
	if !recipient.valid() {
		return Disposition{}, false, ErrInvalidRecipient
	}
	if !id.valid() {
		return Disposition{}, false, ErrInvalidID
	}
	if strings.TrimSpace(action) == "" {
		return Disposition{}, false, ErrDispositionConflict
	}
	var disposition Disposition
	created := false
	err := store.withMailboxLock(recipient, func(mailbox mailboxPath) error {
		existing, err := readDisposition(mailbox, recipient, id)
		if err == nil {
			if existing.Action != action {
				return ErrDispositionConflict
			}
			disposition = existing
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := readMessage(mailbox.pendingPath().messagePath(id), recipient, id); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if _, err := readMessage(mailbox.handledPath().messagePath(id), recipient, id); err != nil {
				return err
			}
		}
		disposition = Disposition{
			MessageID: id, Recipient: recipient, Action: action, State: DispositionStarted,
		}
		if err := os.MkdirAll(filepath.Join(mailbox.String(), "dispositions"), 0o700); err != nil {
			return err
		}
		if err := syncDirectory(messageDirectoryPath{value: mailbox.String()}); err != nil {
			return err
		}
		if err := writeDisposition(mailbox, disposition); err != nil {
			return err
		}
		created = true
		return nil
	})
	return disposition, created, err
}

// CompleteDisposition records the verified result of a started action.
func (store *Store) CompleteDisposition(
	recipient Recipient, id MessageID, outcome string,
) (Disposition, error) {
	if store == nil || store.root == (rootPath{}) {
		return Disposition{}, ErrInvalidRoot
	}
	if !recipient.valid() {
		return Disposition{}, ErrInvalidRecipient
	}
	if !id.valid() {
		return Disposition{}, ErrInvalidID
	}
	if strings.TrimSpace(outcome) == "" {
		return Disposition{}, ErrDispositionConflict
	}
	var disposition Disposition
	err := store.withMailboxLock(recipient, func(mailbox mailboxPath) error {
		existing, err := readDisposition(mailbox, recipient, id)
		if err != nil {
			return err
		}
		if existing.State == DispositionComplete {
			if existing.Outcome != outcome {
				return ErrDispositionConflict
			}
			disposition = existing
			return nil
		}
		existing.State = DispositionComplete
		existing.Outcome = outcome
		if err := writeDisposition(mailbox, existing); err != nil {
			return err
		}
		disposition = existing
		return nil
	})
	return disposition, err
}
