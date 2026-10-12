package inbox

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestConfirmExistingPreservesPendingAndHandledMessages(t *testing.T) {
	for _, location := range []string{"pending", "handled"} {
		t.Run(location, func(t *testing.T) {
			store := newTestStore(t)
			recipient := mustRecipient(t, "worker")
			original, err := store.Send(recipient, "reply", "Result")
			if err != nil {
				t.Fatal(err)
			}
			if location == "handled" {
				if err := store.Acknowledge(recipient, original.ID); err != nil {
					t.Fatal(err)
				}
			}
			var synced []string
			store.syncPath = func(path string) error {
				synced = append(synced, path)
				return nil
			}
			confirmed, err := store.ConfirmExisting(recipient, "reply", "Result")
			if err != nil || !reflect.DeepEqual(confirmed, original) {
				t.Fatalf("confirmation = %#v, %v; original = %#v", confirmed, err, original)
			}
			if want := mailboxSyncPaths(store, recipient); !reflect.DeepEqual(synced, want) {
				t.Fatalf("synced = %v; want %v", synced, want)
			}
			pending, err := store.List(recipient)
			wantCount := 1
			if location == "handled" {
				wantCount = 0
				if _, err := store.ReadHandled(recipient, original.ID); err != nil {
					t.Fatalf("handled message changed: %v", err)
				}
			}
			if err != nil || len(pending) != wantCount {
				t.Fatalf("pending = %#v, %v", pending, err)
			}
		})
	}
}

func TestConfirmExistingNeverPublishesMissingOrChangedMessages(t *testing.T) {
	for _, test := range []string{"absent", "changed key", "changed body", "changed recipient", "blank key", "blank body"} {
		t.Run(test, func(t *testing.T) {
			store := newTestStore(t)
			recipient := mustRecipient(t, "worker")
			if test != "absent" {
				if _, err := store.Send(recipient, "reply", "Result"); err != nil {
					t.Fatal(err)
				}
			}
			key, body := "reply", "Result"
			want := os.ErrNotExist
			switch test {
			case "changed key":
				key = "missing"
			case "changed body":
				body, want = "Different", ErrKeyConflict
			case "changed recipient":
				recipient = mustRecipient(t, "other")
			case "blank key":
				key, want = "", ErrMissingKey
			case "blank body":
				body, want = " \n", ErrBlankBody
			}
			if _, err := store.ConfirmExisting(recipient, key, body); !errors.Is(err, want) {
				t.Fatalf("confirmation = %v; want %v", err, want)
			}
			pending, err := store.List(recipient)
			wantCount := 1
			if test == "changed recipient" || test == "absent" {
				wantCount = 0
			}
			if err != nil || len(pending) != wantCount {
				t.Fatalf("confirmation published message: %#v, %v", pending, err)
			}
		})
	}
}

func TestConfirmExistingRetriesEverySynchronizationFailure(t *testing.T) {
	store := newTestStore(t)
	recipient := mustRecipient(t, "worker")
	original, err := store.Send(recipient, "reply", "Result")
	if err != nil {
		t.Fatal(err)
	}
	for _, failedPath := range mailboxSyncPaths(store, recipient) {
		t.Run(failedPath, func(t *testing.T) {
			failure := errors.New("directory synchronization failed")
			store.syncPath = func(path string) error {
				if path == failedPath {
					return failure
				}
				return nil
			}
			if _, err := store.ConfirmExisting(recipient, "reply", "Result"); !errors.Is(
				err,
				failure,
			) {
				t.Fatalf("uncertain confirmation = %v", err)
			}
			fresh, _ := New(store.Root())
			confirmed, err := fresh.ConfirmExisting(recipient, "reply", "Result")
			if err != nil || confirmed.ID != original.ID {
				t.Fatalf("retry = %#v, %v", confirmed, err)
			}
		})
	}
}
