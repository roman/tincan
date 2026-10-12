package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/stateversion"
	"github.com/roman/tincan/pkg/taskprotocol"
)

type cli struct {
	TaskRoot    string         `help:"Absolute Concierge state root." required:""`
	MessageRoot string         `help:"Absolute Tincan message root." required:""`
	Read        messageCommand `cmd:"" help:"Read an assignment for the current worker generation."`
	Ack         messageCommand `cmd:"" help:"Acknowledge an assignment for the current worker generation."`
}

type messageCommand struct {
	TaskID     string `arg:"" help:"Local task ID."`
	MessageID  string `arg:"" help:"Pending assignment ID."`
	Generation string `help:"Generation supplied to this worker." required:""`
}

type assignmentStatus struct {
	state string
	body  string
}

type operation int

const (
	readOperation operation = iota
	acknowledgeOperation
)

func readAssignment(options cli, input messageCommand, action operation) (assignmentStatus, error) {
	if _, err := firstmate.ParseTaskID(input.TaskID); err != nil {
		return assignmentStatus{}, err
	}
	if err := firstmate.ValidateSpawnGeneration(input.Generation); err != nil {
		return assignmentStatus{}, err
	}
	id, err := inbox.ParseMessageID(input.MessageID)
	if err != nil {
		return assignmentStatus{}, err
	}
	store, err := inbox.New(options.MessageRoot)
	if err != nil {
		return assignmentStatus{}, err
	}
	recipient, _ := inbox.ParseRecipient(input.TaskID)
	registry, err := crew.New(options.TaskRoot)
	if err != nil {
		return assignmentStatus{}, err
	}
	status := assignmentStatus{}
	err = registry.WithCurrentTask(input.TaskID, func(task *crew.CurrentTask) error {
		if task.Record().SpawnGeneration != input.Generation {
			status.state = "stale"
			return nil
		}
		message, err := store.Read(recipient, id)
		if action == acknowledgeOperation && errors.Is(err, os.ErrNotExist) {
			if err := store.Acknowledge(recipient, id); err != nil {
				return err
			}
			status.state = "acknowledged"
			return nil
		}
		if err != nil {
			return err
		}
		assignment, err := taskprotocol.DecodeAssignment(message.Body, options.TaskRoot, input.TaskID)
		if err != nil {
			return err
		}
		if assignment.SpawnGeneration != input.Generation {
			if action == acknowledgeOperation {
				if err := store.Acknowledge(recipient, id); err != nil {
					return err
				}
			}
			status.state = "stale"
			return nil
		}
		if action == acknowledgeOperation {
			if err := store.Acknowledge(recipient, id); err != nil {
				return err
			}
			status.state = "acknowledged"
			return nil
		}
		status.state = "current"
		status.body = assignment.Body
		return nil
	})
	return status, err
}

func run(arguments []string, output io.Writer) int {
	var options cli
	parser, err := kong.New(&options, kong.Name("tincan-task-receive"),
		kong.Writers(output, io.Discard))
	if err != nil {
		fmt.Fprintf(output, "error: %q\n", err)
		return 2
	}
	selected, err := parser.Parse(arguments)
	if err != nil {
		fmt.Fprintf(output, "error: %q\n", err)
		return 2
	}
	if !filepath.IsAbs(options.TaskRoot) || filepath.Clean(options.TaskRoot) != options.TaskRoot ||
		!filepath.IsAbs(options.MessageRoot) || filepath.Clean(options.MessageRoot) != options.MessageRoot {
		fmt.Fprintln(output, "error: state roots must be clean absolute paths")
		return 2
	}
	options.TaskRoot, err = filepath.EvalSymlinks(options.TaskRoot)
	if err != nil {
		fmt.Fprintf(output, "error: %q\n", err)
		return 1
	}
	command := options.Read
	action := readOperation
	switch selected.Command() {
	case "read <task-id> <message-id>":
	case "ack <task-id> <message-id>":
		command = options.Ack
		action = acknowledgeOperation
	default:
		fmt.Fprintln(output, "error: unknown command")
		return 2
	}
	versionGuard, err := stateversion.AcquireLegacy(context.Background(), options.TaskRoot)
	if err != nil {
		fmt.Fprintf(output, "error: %q\nhelp: install a Tincan command compatible with this task state\n", err)
		return 1
	}
	defer versionGuard.Close()
	status, err := readAssignment(options, command, action)
	if err != nil {
		fmt.Fprintf(output, "error: %q\nhelp: inspect the task assignment and generation\n", err)
		return 1
	}
	fmt.Fprintf(output, "status: %s\nid: %s\ntask: %s\n",
		status.state, command.MessageID, command.TaskID)
	if status.state == "current" {
		quoted, _ := json.Marshal(status.body)
		fmt.Fprintf(output, "spawn_generation: %s\nbody: %s\n", command.Generation, quoted)
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }
