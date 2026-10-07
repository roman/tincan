package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/inbox"
)

type CLI struct {
	Root string `help:"Directory that stores mailboxes." env:"TINCAN_HOME" required:""`
	Send struct {
		Recipient string  `arg:"" help:"Recipient identifier."`
		Key       string  `help:"Stable key for retry-safe sends."`
		Body      *string `help:"Message text."`
		BodyFile  string  `help:"Read message text from this file; use - for stdin."`
	} `cmd:"" help:"Store a message for a recipient."`
	List struct {
		Recipient string `arg:"" help:"Recipient identifier."`
	} `cmd:"" help:"List pending messages."`
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

func messageBody(command *CLI) (string, error) {
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
		data, err := io.ReadAll(os.Stdin)
		return string(data), err
	}
	data, err := os.ReadFile(command.Send.BodyFile)
	return string(data), err
}

func run(command *CLI, selected string) error {
	store, err := inbox.New(command.Root)
	if err != nil {
		return err
	}
	switch selected {
	case "send <recipient>":
		body, err := messageBody(command)
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
		fmt.Printf("id: %s\nrecipient: %s\n", message.ID, message.Recipient)
	case "list <recipient>":
		recipient, err := inbox.ParseRecipient(command.List.Recipient)
		if err != nil {
			return err
		}
		messages, err := store.List(recipient)
		if err != nil {
			return err
		}
		fmt.Printf("count: %d\n", len(messages))
		if len(messages) > 0 {
			fmt.Printf("messages[%d]{id,key,created_at}:\n", len(messages))
			for _, message := range messages {
				fmt.Printf("  %s,%s,%s\n", message.ID, quote(message.Key), message.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
			}
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
		fmt.Printf("id: %s\nrecipient: %s\nkey: %s\nbody: %s\ncreated_at: %s\n", message.ID, message.Recipient, quote(message.Key), quote(message.Body), message.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"))
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
		fmt.Printf("acknowledged: %s\n", command.Ack.ID)
	default:
		return fmt.Errorf("unknown command %q", selected)
	}
	return nil
}

func main() {
	var command CLI
	context := kong.Parse(&command, kong.Description("A durable mailbox for independent agents."))
	if err := run(&command, context.Command()); err != nil {
		fmt.Fprintln(os.Stderr, "tincan-inbox:", err)
		os.Exit(1)
	}
}
