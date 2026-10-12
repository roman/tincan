// Package resident watches durable Tincan mailboxes for controller-owned wakes.
package resident

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
)

type Watcher struct {
	Store      *inbox.Store
	Recipient  inbox.Recipient
	Target     string
	Doorbell   notify.Doorbell
	RetryDelay time.Duration
	Report     func(error)
}

// Run watches one mailbox and retries doorbells until its messages are handled.
func (watcher Watcher) Run(ctx context.Context, ready chan<- struct{}) error {
	if watcher.Store == nil || watcher.Doorbell == nil {
		return errors.New("mailbox watcher needs a store and doorbell")
	}
	if _, err := inbox.ParseRecipient(watcher.Recipient.String()); err != nil {
		return err
	}
	if _, err := notify.ParseTarget(watcher.Target); err != nil {
		return err
	}
	delay := watcher.RetryDelay
	if delay <= 0 {
		delay = 5 * time.Second
	}
	pending := filepath.Join(watcher.Store.Root(), "inbox", watcher.Recipient.String(), "pending")
	if err := os.MkdirAll(pending, 0o700); err != nil {
		return err
	}
	files, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer files.Close()
	if err := files.Add(pending); err != nil {
		return err
	}
	var retry *time.Timer
	var retryEvents <-chan time.Time
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()
	schedule := func() {
		if retry == nil {
			retry = time.NewTimer(delay)
		} else {
			retry.Reset(delay)
		}
		retryEvents = retry.C
	}
	check := func() {
		messages, err := watcher.Store.List(watcher.Recipient)
		if err != nil {
			if watcher.Report != nil {
				watcher.Report(err)
			}
			schedule()
			return
		}
		if len(messages) == 0 {
			if retry != nil {
				retry.Stop()
			}
			retryEvents = nil
			return
		}
		if err := watcher.Doorbell.Prompt(ctx, watcher.Target, notify.DoorbellText); err != nil &&
			ctx.Err() == nil {
			if watcher.Report != nil {
				watcher.Report(err)
			}
		}
		if ctx.Err() == nil {
			schedule()
		}
	}
	check()
	if ctx.Err() != nil {
		return nil
	}
	if ready != nil {
		close(ready)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-files.Events:
			if !ok {
				return errors.New("mailbox watcher event stream closed")
			}
			if event.Has(fsnotify.Create|fsnotify.Rename) &&
				strings.HasSuffix(event.Name, ".json") {
				check()
			}
		case err, ok := <-files.Errors:
			if !ok {
				return errors.New("mailbox watcher error stream closed")
			}
			if watcher.Report != nil {
				watcher.Report(fmt.Errorf("watch mailbox: %w", err))
			}
			schedule()
		case <-retryEvents:
			check()
		}
	}
}
