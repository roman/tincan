package inbox

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mailboxSyncPaths(store *Store, recipient Recipient) []string {
	mailbox := store.mailboxPath(recipient)
	paths := []string{mailbox.pendingPath().String(), mailbox.handledPath().String()}
	for path := mailbox.String(); ; path = filepath.Dir(path) {
		paths = append(paths, path)
		if path == filepath.Dir(path) {
			return paths
		}
	}
}

func TestSendConfirmsDurabilityAfterEveryDirectoryFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing", "nested", "messages")
	prototype, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	recipient := mustRecipient(t, "worker")
	for _, failedPath := range mailboxSyncPaths(prototype, recipient) {
		t.Run(failedPath, func(t *testing.T) {
			store, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("directory sync failed")
			store.syncPath = func(path string) error {
				if path == failedPath {
					return failure
				}
				return nil
			}
			first, err := store.Send(recipient, failedPath, "bootstrap")
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), failedPath) {
				t.Fatalf("Send error = %v; want failure at %s", err, failedPath)
			}
			fresh, err := New(root)
			if err != nil {
				t.Fatal(err)
			}
			var synced []string
			fresh.syncPath = func(path string) error {
				synced = append(synced, path)
				return nil
			}
			retried, err := fresh.Send(recipient, failedPath, "bootstrap")
			if err != nil || retried.ID != first.ID || !retried.CreatedAt.Equal(first.CreatedAt) {
				t.Fatalf("retry = %#v, %v; first = %#v", retried, err, first)
			}
			if want := mailboxSyncPaths(fresh, recipient); !reflect.DeepEqual(synced, want) {
				t.Fatalf("synced directories = %v; want %v", synced, want)
			}
		})
	}
}

func TestSendHandledRetryConfirmsBothMessageDirectories(t *testing.T) {
	store := newTestStore(t)
	recipient := mustRecipient(t, "worker")
	first, err := store.Send(recipient, "bootstrap", "body")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Acknowledge(recipient, first.ID); err != nil {
		t.Fatal(err)
	}
	var synced []string
	store.syncPath = func(path string) error {
		synced = append(synced, path)
		return nil
	}
	retried, err := store.Send(recipient, "bootstrap", "body")
	if err != nil || retried.ID != first.ID {
		t.Fatalf("handled retry = %#v, %v", retried, err)
	}
	if want := mailboxSyncPaths(store, recipient); !reflect.DeepEqual(synced, want) {
		t.Fatalf("synced directories = %v; want %v", synced, want)
	}
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 0 {
		t.Fatalf("handled message restored to pending: %v, %v", pending, err)
	}
}
