package inbox

import (
	"errors"
	"os"
	"strings"
)

var ErrMissingKey = errors.New("existing-message confirmation requires a key")

// ConfirmExisting confirms durability without creating or reopening a message.
func (store *Store) ConfirmExisting(recipient Recipient, key, body string) (Message, error) {
	if store == nil || store.root == (rootPath{}) {
		return Message{}, ErrInvalidRoot
	}
	if !recipient.valid() {
		return Message{}, ErrInvalidRecipient
	}
	if key == "" {
		return Message{}, ErrMissingKey
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
		if !found {
			return os.ErrNotExist
		}
		message = existing
		return store.syncMailbox(mailbox)
	})
	return message, err
}
