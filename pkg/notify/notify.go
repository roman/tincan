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

// Notifier checks one mailbox and rings its associated agent target.
type Notifier struct {
	Store     Store
	Doorbell  Doorbell
	Recipient inbox.Recipient
	Target    Target
}

// NotifyOnce sends only a doorbell; the durable message stays in the inbox.
func (notifier Notifier) NotifyOnce(ctx context.Context) error {
	if !notifier.Target.valid() {
		return ErrInvalidTarget
	}
	messages, err := notifier.Store.List(notifier.Recipient)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrList, err)
	}
	if len(messages) == 0 {
		return nil
	}
	if err := notifier.Doorbell.Prompt(ctx, notifier.Target.String(), DoorbellText); err != nil {
		return fmt.Errorf("%w: %w", ErrPrompt, err)
	}
	return nil
}

// Run retries the doorbell until messages are acknowledged or the context ends.
func (notifier Notifier) Run(
	ctx context.Context,
	interval time.Duration,
	report func(error),
) error {
	if interval <= 0 {
		return errors.New("interval must be positive")
	}
	if !notifier.Target.valid() {
		return ErrInvalidTarget
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := notifier.NotifyOnce(ctx); err != nil {
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
