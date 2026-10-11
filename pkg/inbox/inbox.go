// Package inbox stores messages until a recipient acknowledges them.
package inbox

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
)

var (
	ErrInvalidRecipient = errors.New("invalid recipient")
	ErrInvalidID        = errors.New("invalid message ID")
	ErrBlankBody        = errors.New("blank message body")
	ErrKeyConflict      = errors.New("message key already has a different body")
	ErrCorruptMessage   = errors.New("message record does not match its inbox path")
)

// Message is a durable record addressed to one recipient.
type Message struct {
	ID        MessageID `json:"id"`
	Recipient Recipient `json:"recipient"`
	Key       string    `json:"key,omitempty"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// UnmarshalJSON rejects records without validated identifiers.
func (message *Message) UnmarshalJSON(data []byte) error {
	type wireMessage Message
	var decoded wireMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if !decoded.ID.valid() {
		return ErrInvalidID
	}
	if !decoded.Recipient.valid() {
		return ErrInvalidRecipient
	}
	*message = Message(decoded)
	return nil
}

// Store manages durable mailboxes below one root directory. New creates valid stores; message operations reject nil and zero-value stores.
type Store struct{ root rootPath }

// New validates the root and creates a store without touching the filesystem.
func New(root string) (*Store, error) {
	path, err := newRootPath(root)
	if err != nil {
		return nil, err
	}
	return &Store{root: path}, nil
}

// Root returns the store’s normalized absolute directory path.
func (store *Store) Root() string { return store.root.String() }

func generateMessageID(key string) (MessageID, error) {
	if key != "" {
		// A retry key derives a stable k- ID, so retries find the record and changed bodies conflict.
		digest := sha256.Sum256([]byte(key))
		return MessageID{value: "k-" + hex.EncodeToString(digest[:])}, nil
	}
	// An empty key gets a fresh random m- ID, so unkeyed sends create independent records.
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return MessageID{}, err
	}
	return MessageID{value: "m-" + hex.EncodeToString(random)}, nil
}

func (store *Store) mailboxPath(recipient Recipient) mailboxPath {
	return store.root.mailboxPath(recipient)
}

func (store *Store) withMailboxLock(recipient Recipient, operation func(mailboxPath) error) error {
	mailbox := store.mailboxPath(recipient)
	for _, directory := range []messageDirectoryPath{mailbox.pendingPath(), mailbox.handledPath()} {
		if err := os.MkdirAll(directory.String(), 0o700); err != nil {
			return err
		}
	}
	lock, err := os.OpenFile(mailbox.lockPath().String(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	// Separate CLI processes need a cross-process lock; sync.Mutex is process-local.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation(mailbox)
}

func readMessage(path filePath, recipient Recipient, id MessageID) (Message, error) {
	var message Message
	data, err := os.ReadFile(path.String())
	if err != nil {
		return message, err
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return message, fmt.Errorf("decode %s: %w", path, err)
	}
	// A record identity that disagrees with its mailbox path is corrupt or misplaced and cannot be trusted.
	isCorruptedMessage := message.Recipient != recipient || message.ID != id
	if isCorruptedMessage {
		return Message{}, fmt.Errorf("%w: %s", ErrCorruptMessage, path)
	}
	return message, nil
}

func syncDirectory(path messageDirectoryPath) error {
	directory, err := os.Open(path.String())
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func findExistingMessage(
	mailbox mailboxPath,
	recipient Recipient,
	id MessageID,
	key, body string,
) (Message, bool, error) {
	for _, directory := range []messageDirectoryPath{mailbox.pendingPath(), mailbox.handledPath()} {
		existing, err := readMessage(directory.messagePath(id), recipient, id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Message{}, false, err
		}
		matchesRequest := existing.Recipient == recipient && existing.Key == key &&
			existing.Body == body
		if !matchesRequest {
			return Message{}, false, ErrKeyConflict
		}
		return existing, true, nil
	}
	return Message{}, false, nil
}

func persistPendingMessage(pending messageDirectoryPath, message Message) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(pending.String(), ".writing-*")
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
	// The link publishes a synced file without exposing a partial message.
	if err := os.Link(temporary.Name(), pending.messagePath(message.ID).String()); err != nil {
		return err
	}
	return syncDirectory(pending)
}

// Send stores a nonblank message until acknowledgement. A repeated key with the same body returns the original record.
func (store *Store) Send(recipient Recipient, key, body string) (Message, error) {
	if store == nil || store.root == (rootPath{}) {
		return Message{}, ErrInvalidRoot
	}
	if !recipient.valid() {
		return Message{}, ErrInvalidRecipient
	}
	if strings.TrimSpace(body) == "" {
		return Message{}, ErrBlankBody
	}
	id, err := generateMessageID(key)
	if err != nil {
		return Message{}, err
	}
	var message Message
	err = store.withMailboxLock(recipient, func(mailbox mailboxPath) error {
		existing, found, err := findExistingMessage(mailbox, recipient, id, key, body)
		if err != nil {
			return err
		}
		if found {
			message = existing
			return nil
		}
		message = Message{
			ID:        id,
			Recipient: recipient,
			Key:       key,
			Body:      body,
			CreatedAt: time.Now().UTC(),
		}
		return persistPendingMessage(mailbox.pendingPath(), message)
	})
	return message, err
}

// Read returns a pending message for the recipient and ID.
func (store *Store) Read(recipient Recipient, id MessageID) (Message, error) {
	if store == nil || store.root == (rootPath{}) {
		return Message{}, ErrInvalidRoot
	}
	if !recipient.valid() {
		return Message{}, ErrInvalidRecipient
	}
	if !id.valid() {
		return Message{}, ErrInvalidID
	}
	return readMessage(store.mailboxPath(recipient).pendingPath().messagePath(id), recipient, id)
}

// ReadHandled returns a previously acknowledged message for retry validation.
func (store *Store) ReadHandled(recipient Recipient, id MessageID) (Message, error) {
	if store == nil || store.root == (rootPath{}) {
		return Message{}, ErrInvalidRoot
	}
	if !recipient.valid() {
		return Message{}, ErrInvalidRecipient
	}
	if !id.valid() {
		return Message{}, ErrInvalidID
	}
	return readMessage(store.mailboxPath(recipient).handledPath().messagePath(id), recipient, id)
}

// List returns pending messages in creation order.
func (store *Store) List(recipient Recipient) ([]Message, error) {
	if store == nil || store.root == (rootPath{}) {
		return nil, ErrInvalidRoot
	}
	if !recipient.valid() {
		return nil, ErrInvalidRecipient
	}
	directory := store.mailboxPath(recipient).pendingPath()
	entries, err := os.ReadDir(directory.String())
	if errors.Is(err, os.ErrNotExist) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		messageIDText := strings.TrimSuffix(entry.Name(), ".json")
		id, parseErr := ParseMessageID(messageIDText)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: invalid filename %s", ErrCorruptMessage, entry.Name())
		}
		message, readErr := readMessage(directory.messagePath(id), recipient, id)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		} // An acknowledgement may move it during the scan.
		if readErr != nil {
			return nil, readErr
		}
		messages = append(messages, message)
	}
	// Creation time gives delivery order; ID makes equal timestamps deterministic.
	sort.Slice(messages, func(left, right int) bool {
		if messages[left].CreatedAt.Equal(messages[right].CreatedAt) {
			return messages[left].ID.String() < messages[right].ID.String()
		}
		return messages[left].CreatedAt.Before(messages[right].CreatedAt)
	})
	return messages, nil
}

func syncAcknowledgeDirectories(mailbox mailboxPath) error {
	handled, pending := mailbox.handledPath(), mailbox.pendingPath()
	// Rename changes entries in both directories; sync both before reporting acknowledgement.
	if err := syncDirectory(handled); err != nil {
		return fmt.Errorf("sync handled directory %s: %w", handled, err)
	}
	if err := syncDirectory(pending); err != nil {
		return fmt.Errorf("sync pending directory %s: %w", pending, err)
	}
	return nil
}

// Acknowledge marks a message handled and succeeds on a repeated acknowledgement.
func (store *Store) Acknowledge(recipient Recipient, id MessageID) error {
	if store == nil || store.root == (rootPath{}) {
		return ErrInvalidRoot
	}
	if !recipient.valid() {
		return ErrInvalidRecipient
	}
	if !id.valid() {
		return ErrInvalidID
	}
	return store.withMailboxLock(recipient, func(mailbox mailboxPath) error {
		pending := mailbox.pendingPath()
		handled := mailbox.handledPath()
		source, destination := pending.messagePath(id), handled.messagePath(id)
		if _, err := os.Stat(destination.String()); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(source.String(), destination.String()); err != nil {
			return err
		}
		return syncAcknowledgeDirectories(mailbox)
	})
}
