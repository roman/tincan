package inbox

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseRecipient(t *testing.T) {
	for _, test := range []struct {
		name  string
		valid bool
	}{{"worker-1", true}, {"a.b_c", true}, {"", false}, {"..", false}, {"../bad", false}, {".hidden", false}} {
		t.Run(test.name, func(t *testing.T) {
			value, err := ParseRecipient(test.name)
			if (err == nil) != test.valid {
				t.Fatalf("ParseRecipient(%q): %v", test.name, err)
			}
			if test.valid && value.String() != test.name {
				t.Fatalf("value = %q", value.String())
			}
		})
	}
}
func TestParseMessageID(t *testing.T) {
	for _, test := range []struct {
		name  string
		valid bool
	}{{"k-" + strings.Repeat("a", 64), true}, {"m-" + strings.Repeat("b", 32), true}, {"", false}, {"../bad", false}, {"m-123", false}, {"k-" + strings.Repeat("g", 64), false}} {
		t.Run(test.name, func(t *testing.T) {
			value, err := ParseMessageID(test.name)
			if (err == nil) != test.valid {
				t.Fatalf("ParseMessageID(%q): %v", test.name, err)
			}
			if test.valid && value.String() != test.name {
				t.Fatalf("value = %q", value.String())
			}
		})
	}
}
func TestStoreRejectsInvalidRootAndZeroValues(t *testing.T) {
	if _, err := New(""); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("empty root: %v", err)
	}
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Send(Recipient{}, "", "body"); !errors.Is(err, ErrInvalidRecipient) {
		t.Fatalf("zero recipient: %v", err)
	}
	recipient, _ := ParseRecipient("worker")
	if _, err := store.Read(recipient, MessageID{}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("zero ID: %v", err)
	}
	if _, err := (&Store{}).List(recipient); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("zero store: %v", err)
	}
}
func TestMessageJSONRejectsInvalidIdentifiers(t *testing.T) {
	for _, raw := range []string{`{"id":"../bad","recipient":"worker","body":"x"}`, `{"id":"m-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","recipient":"../bad","body":"x"}`, `{"recipient":"worker","body":"x"}`, `{"id":"m-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","body":"x"}`} {
		var message Message
		if err := json.Unmarshal([]byte(raw), &message); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
