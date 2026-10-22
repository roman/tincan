// Package tincancli implements generic durable messaging commands.
package tincancli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/roman/tincan/pkg/inbox"
)

type InboxCommands struct {
	Send struct {
		Recipient string  `arg:"" help:"Recipient identifier."`
		Key       string  `help:"Stable key for retry-safe sends."`
		Body      *string `help:"Message text."`
		BodyFile  string  `help:"Read message text from this file; use - for stdin."`
	} `cmd:"" help:"Store a message for a recipient."`
	List struct {
		Recipient string `arg:"" help:"Recipient identifier."`
		Full      bool   `help:"Include full keys and creation times using the legacy list format."`
	} `cmd:"" help:"List pending messages."`
	Wait struct {
		Recipient string        `arg:"" help:"Recipient identifier."`
		Timeout   time.Duration `help:"Maximum time to wait before a checkpoint." default:"30s"`
		Interval  time.Duration `help:"Time between inbox checks." default:"250ms"`
	} `cmd:"" help:"Wait for pending messages or a bounded checkpoint."`
	Read struct {
		Recipient string `arg:"" help:"Recipient identifier."`
		ID        string `arg:"" help:"Message identifier."`
	} `cmd:"" help:"Read a pending message."`
	Ack struct {
		Recipient string `arg:"" help:"Recipient identifier."`
		ID        string `arg:"" help:"Message identifier."`
	} `cmd:"" help:"Acknowledge a message after handling it."`
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func messageBody(command *InboxCommands, stdin io.Reader) (string, error) {
	if command.Send.Body != nil && command.Send.BodyFile != "" {
		return "", errors.New("choose either --body or --body-file")
	}
	if command.Send.Body != nil {
		return *command.Send.Body, nil
	}
	if command.Send.BodyFile == "" {
		return "", errors.New("provide --body or --body-file")
	}
	if command.Send.BodyFile == "-" {
		data, err := io.ReadAll(stdin)
		return string(data), err
	}
	data, err := os.ReadFile(command.Send.BodyFile)
	return string(data), err
}

func waitForPending(
	ctx context.Context, store *inbox.Store, recipient inbox.Recipient,
	timeout, interval time.Duration,
) (int, error) {
	if timeout <= 0 || interval <= 0 {
		return 0, errors.New("timeout and interval must be positive")
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		messages, err := store.List(recipient)
		if err != nil {
			return 0, err
		}
		if len(messages) > 0 {
			return len(messages), nil
		}
		select {
		case <-waitContext.Done():
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return 0, nil
		case <-ticker.C:
		}
	}
}

func runInbox(
	ctx context.Context,
	root string,
	command *InboxCommands,
	selected string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	store, err := inbox.New(root)
	if err != nil {
		return err
	}
	switch selected {
	case "send <recipient>":
		body, err := messageBody(command, stdin)
		if err != nil {
			return err
		}
		recipient, err := inbox.ParseRecipient(command.Send.Recipient)
		if err != nil {
			return err
		}
		message, err := store.Send(recipient, command.Send.Key, body)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "id: %s\nrecipient: %s\n", message.ID, message.Recipient)
	case "list <recipient>":
		recipient, err := inbox.ParseRecipient(command.List.Recipient)
		if err != nil {
			return err
		}
		messages, err := store.List(recipient)
		if err != nil {
			return err
		}
		if !command.List.Full {
			writeCompactList(stdout, store.Root(), recipient, messages)
			return nil
		}
		fmt.Fprintf(stdout, "count: %d\n", len(messages))
		if len(messages) > 0 {
			fmt.Fprintf(stdout, "messages[%d]{id,key,created_at}:\n", len(messages))
			for _, message := range messages {
				fmt.Fprintf(stdout,
					"  %s,%s,%s\n",
					message.ID,
					quote(message.Key),
					message.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
				)
			}
		}
	case "wait <recipient>":
		recipient, err := inbox.ParseRecipient(command.Wait.Recipient)
		if err != nil {
			return err
		}
		count, err := waitForPending(ctx, store, recipient,
			command.Wait.Timeout, command.Wait.Interval)
		if err != nil {
			return err
		}
		if count > 0 {
			fmt.Fprintf(stdout, "status: pending\ncount: %d\n", count)
		} else {
			fmt.Fprint(stdout, "status: checkpoint\ncount: 0\n")
		}
	case "read <recipient> <id>":
		recipient, err := inbox.ParseRecipient(command.Read.Recipient)
		if err != nil {
			return err
		}
		id, err := inbox.ParseMessageID(command.Read.ID)
		if err != nil {
			return err
		}
		message, err := store.Read(recipient, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout,
			"id: %s\nrecipient: %s\nkey: %s\nbody: %s\ncreated_at: %s\n",
			message.ID,
			message.Recipient,
			quote(message.Key),
			quote(message.Body),
			message.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		)
	case "ack <recipient> <id>":
		recipient, err := inbox.ParseRecipient(command.Ack.Recipient)
		if err != nil {
			return err
		}
		id, err := inbox.ParseMessageID(command.Ack.ID)
		if err != nil {
			return err
		}
		if err := store.Acknowledge(recipient, id); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "acknowledged: %s\n", command.Ack.ID)
	default:
		return fmt.Errorf("unknown command %q", selected)
	}
	return nil
}
