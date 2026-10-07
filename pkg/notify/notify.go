package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/roman/tincan/pkg/inbox"
)

const DoorbellText = "Pending Tincan inbox messages are available. Read and acknowledge them."

var (
	ErrList   = errors.New("list inbox failed")
	ErrPrompt = errors.New("send doorbell failed")
)

type Store interface {
	List(inbox.Recipient) ([]inbox.Message, error)
}
type Doorbell interface {
	Prompt(context.Context, string, string) error
}

// NotifyOnce sends only a doorbell; the durable message stays in the inbox.
func NotifyOnce(ctx context.Context, store Store, bell Doorbell, recipient inbox.Recipient) error {
	messages, err := store.List(recipient)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrList, err)
	}
	if len(messages) == 0 {
		return nil
	}
	if err := bell.Prompt(ctx, recipient.String(), DoorbellText); err != nil {
		return fmt.Errorf("%w: %w", ErrPrompt, err)
	}
	return nil
}

// Run retries the doorbell until messages are acknowledged or the context ends.
func Run(ctx context.Context, store Store, bell Doorbell, recipient inbox.Recipient, interval time.Duration, report func(error)) error {
	if interval <= 0 {
		return errors.New("interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := NotifyOnce(ctx, store, bell, recipient); err != nil {
			if errors.Is(err, inbox.ErrInvalidRecipient) {
				return err
			}
			if report != nil {
				report(err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
