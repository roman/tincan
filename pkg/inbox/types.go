package inbox

import (
	"encoding/json"
	"errors"
	"strings"
)

var ErrInvalidRoot = errors.New("invalid inbox root")

// Recipient is a mailbox identifier. ParseRecipient creates valid values; the zero value is invalid and rejected by store methods.
type Recipient struct{ value string }

// ParseRecipient accepts an identifier that is safe as one mailbox path component.
func ParseRecipient(raw string) (Recipient, error) {
	if raw == "" || raw[0] == '.' {
		return Recipient{}, ErrInvalidRecipient
	}
	for _, character := range raw {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return Recipient{}, ErrInvalidRecipient
	}
	return Recipient{value: raw}, nil
}

// String returns the recipient identifier.
func (recipient Recipient) String() string { return recipient.value }
func (recipient Recipient) valid() bool    { return recipient.value != "" }

// MarshalJSON encodes a validated recipient as a JSON string.
func (recipient Recipient) MarshalJSON() ([]byte, error) {
	if !recipient.valid() {
		return nil, ErrInvalidRecipient
	}
	return json.Marshal(recipient.value)
}

// UnmarshalJSON parses and validates a recipient from a JSON string.
func (recipient *Recipient) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseRecipient(raw)
	if err != nil {
		return err
	}
	*recipient = parsed
	return nil
}

// MessageID identifies a message. ParseMessageID and Send create valid values; the zero value is invalid and rejected by store methods.
type MessageID struct{ value string }

// ParseMessageID accepts the key-derived or random identifier formats used by Store.
func ParseMessageID(raw string) (MessageID, error) {
	var expected int
	switch {
	case strings.HasPrefix(raw, "k-"):
		expected = 64
	case strings.HasPrefix(raw, "m-"):
		expected = 32
	default:
		return MessageID{}, ErrInvalidID
	}
	if len(raw) != expected+2 {
		return MessageID{}, ErrInvalidID
	}
	for _, character := range raw[2:] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return MessageID{}, ErrInvalidID
		}
	}
	return MessageID{value: raw}, nil
}

// String returns the message identifier.
func (id MessageID) String() string { return id.value }
func (id MessageID) valid() bool    { return id.value != "" }

// MarshalJSON encodes a validated message ID as a JSON string.
func (id MessageID) MarshalJSON() ([]byte, error) {
	if !id.valid() {
		return nil, ErrInvalidID
	}
	return json.Marshal(id.value)
}

// UnmarshalJSON parses and validates a message ID from a JSON string.
func (id *MessageID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := ParseMessageID(raw)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
