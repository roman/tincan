package firstmate

import (
	"context"
	"fmt"

	"github.com/roman/tincan/pkg/inbox"
)

// NewInboxPublisher sends each escalation to the primary agent's durable inbox.
func NewInboxPublisher(root string) (func(context.Context, Escalation) error, error) {
	store, err := inbox.New(root)
	if err != nil {
		return nil, err
	}
	recipient, err := inbox.ParseRecipient("firstmate")
	if err != nil {
		return nil, err
	}
	return func(_ context.Context, escalation Escalation) error {
		key := "firstmate-escalation:" + escalation.StateDir.String() + ":" +
			escalation.TaskID.String() + ":" + escalation.Record.String() + ":" +
			escalation.SpawnGeneration
		body := fmt.Sprintf(
			"kind: task-message-stale\nstate: %s\ntask: %s\nrecord: %s\nspawn_generation: %s\n",
			escalation.StateDir, escalation.TaskID, escalation.Record,
			escalation.SpawnGeneration,
		)
		_, err := store.Send(recipient, key, body)
		return err
	}, nil
}
