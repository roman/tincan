package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
)

type cli struct {
	TaskRoot    string       `help:"Absolute Concierge state root." required:""`
	MessageRoot string       `help:"Absolute Tincan message root." required:""`
	Send        sendCommand  `cmd:"" help:"Send a reply bound to the current worker generation."`
	Check       checkCommand `cmd:"" help:"Check a pending reply against the current worker generation."`
}

type sendCommand struct {
	TaskID       string `arg:"" help:"Bound local task ID."`
	Generation   string `help:"Worker generation from the assignment." required:""`
	Key          string `help:"Stable key for retry-safe sends." required:""`
	AssignmentID string `help:"Assignment to acknowledge after the reply is durable."`
	BodyFile     string `help:"Read reply body from a file; use - for stdin." required:""`
}

type checkCommand struct {
	ID string `arg:"" help:"Pending Tincan message ID."`
}

type replyEnvelope struct {
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Task            string `json:"task"`
	SpawnGeneration string `json:"spawn_generation"`
	AssignmentID    string `json:"assignment_id,omitempty"`
	ReplyKey        string `json:"reply_key,omitempty"`
	Body            string `json:"body"`
}

var errOldGeneration = errors.New("worker generation is no longer current")

func replyBody(path string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", inbox.ErrBlankBody
	}
	return string(data), nil
}

func readBoundAssignment(
	store *inbox.Store, taskRoot, taskID, generation string, id inbox.MessageID,
) error {
	worker, _ := inbox.ParseRecipient(taskID)
	assignment, err := store.Read(worker, id)
	if errors.Is(err, os.ErrNotExist) {
		assignment, err = store.ReadHandled(worker, id)
	}
	if err != nil {
		return err
	}
	envelope, err := taskprotocol.DecodeAssignment(assignment.Body, taskRoot, taskID)
	if err != nil {
		return err
	}
	if envelope.SpawnGeneration != generation {
		return errors.New("assignment does not match this task and generation")
	}
	return nil
}

func send(command cli, stdin io.Reader, stdout io.Writer) error {
	input := command.Send
	if _, err := firstmate.ParseTaskID(input.TaskID); err != nil {
		return err
	}
	if err := firstmate.ValidateSpawnGeneration(input.Generation); err != nil {
		return err
	}
	if strings.TrimSpace(input.Key) == "" {
		return errors.New("reply key is blank")
	}
	body, err := replyBody(input.BodyFile, stdin)
	if err != nil {
		return err
	}
	registry, err := crew.New(command.TaskRoot)
	if err != nil {
		return err
	}
	store, err := inbox.New(command.MessageRoot)
	if err != nil {
		return err
	}
	recipient, _ := inbox.ParseRecipient("firstmate")
	var message inbox.Message
	var assignmentID inbox.MessageID
	if input.AssignmentID != "" {
		assignmentID, err = inbox.ParseMessageID(input.AssignmentID)
		if err != nil {
			return err
		}
	}
	err = registry.WithCurrentTask(input.TaskID, func(task *crew.CurrentTask) error {
		if task.Record().SpawnGeneration != input.Generation {
			return errOldGeneration
		}
		if input.AssignmentID != "" {
			if err := readBoundAssignment(store, command.TaskRoot, input.TaskID,
				input.Generation, assignmentID); err != nil {
				return err
			}
		}
		replyKey := ""
		if input.AssignmentID != "" {
			replyKey = input.Key
		}
		envelope, err := json.Marshal(replyEnvelope{
			Kind: "task-reply", State: filepath.Join(command.TaskRoot, "tasks"),
			Task: input.TaskID, SpawnGeneration: input.Generation,
			AssignmentID: input.AssignmentID, ReplyKey: replyKey, Body: body,
		})
		if err != nil {
			return err
		}
		keySuffix := input.Key
		if input.AssignmentID != "" {
			keySuffix = input.AssignmentID
		}
		key := fmt.Sprintf("task-reply:%s:%s:%s:%s",
			command.TaskRoot, input.TaskID, input.Generation, keySuffix)
		message, err = store.Send(recipient, key, string(envelope))
		if err != nil {
			return err
		}
		if input.AssignmentID != "" {
			worker, _ := inbox.ParseRecipient(input.TaskID)
			return store.Acknowledge(worker, assignmentID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(
		stdout,
		"status: recorded\nid: %s\nrecipient: firstmate\ntask: %s\nspawn_generation: %s\n",
		message.ID,
		input.TaskID,
		input.Generation,
	)
	if input.AssignmentID != "" {
		fmt.Fprintln(stdout, "assignment: acknowledged")
	}
	return nil
}

func check(command cli, stdout io.Writer) error {
	id, err := inbox.ParseMessageID(command.Check.ID)
	if err != nil {
		return err
	}
	store, err := inbox.New(command.MessageRoot)
	if err != nil {
		return err
	}
	recipient, _ := inbox.ParseRecipient("firstmate")
	message, err := store.Read(recipient, id)
	if err != nil {
		return err
	}
	var envelope replyEnvelope
	if err := json.Unmarshal([]byte(message.Body), &envelope); err != nil {
		return fmt.Errorf("invalid task reply: %w", err)
	}
	if envelope.Kind != "task-reply" ||
		envelope.State != filepath.Join(command.TaskRoot, "tasks") ||
		strings.TrimSpace(envelope.Body) == "" {
		return errors.New("message is not a task reply for this state root")
	}
	if _, err := firstmate.ParseTaskID(envelope.Task); err != nil {
		return err
	}
	if err := firstmate.ValidateSpawnGeneration(envelope.SpawnGeneration); err != nil {
		return err
	}
	if envelope.AssignmentID != "" {
		if _, err := inbox.ParseMessageID(envelope.AssignmentID); err != nil {
			return err
		}
		expectedKey := fmt.Sprintf("task-reply:%s:%s:%s:%s",
			command.TaskRoot, envelope.Task, envelope.SpawnGeneration, envelope.AssignmentID)
		if strings.TrimSpace(envelope.ReplyKey) == "" || message.Key != expectedKey {
			return errors.New("bound reply has an invalid key")
		}
	}
	registry, err := crew.New(command.TaskRoot)
	if err != nil {
		return err
	}
	err = registry.WithCurrentTask(envelope.Task, func(task *crew.CurrentTask) error {
		if task.Record().SpawnGeneration != envelope.SpawnGeneration {
			return errOldGeneration
		}
		if envelope.AssignmentID != "" {
			assignmentID, err := inbox.ParseMessageID(envelope.AssignmentID)
			if err != nil {
				return err
			}
			if err := readBoundAssignment(store, command.TaskRoot, envelope.Task,
				envelope.SpawnGeneration, assignmentID); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errOldGeneration) || errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, crew.ErrStaleRecord) {
		fmt.Fprintf(stdout, "status: stale\nid: %s\ntask: %s\n",
			id, envelope.Task)
		return nil
	}
	if err != nil {
		return err
	}
	quotedBody, _ := json.Marshal(envelope.Body)
	fmt.Fprintf(stdout,
		"status: current\nid: %s\ntask: %s\nspawn_generation: %s\nbody: %s\n",
		id, envelope.Task, envelope.SpawnGeneration, quotedBody)
	return nil
}

func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var command cli
	parser, err := kong.New(&command, kong.Name("tincan-task-reply"),
		kong.Writers(stdout, stderr))
	if err != nil {
		fmt.Fprintln(stderr, "tincan-task-reply:", err)
		return 2
	}
	selected, err := parser.Parse(arguments)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-task-reply:", err)
		return 2
	}
	if !filepath.IsAbs(command.TaskRoot) || !filepath.IsAbs(command.MessageRoot) ||
		filepath.Clean(command.TaskRoot) != command.TaskRoot ||
		filepath.Clean(command.MessageRoot) != command.MessageRoot {
		fmt.Fprintln(stderr, "tincan-task-reply: state roots must be clean absolute paths")
		return 2
	}
	command.TaskRoot, err = filepath.EvalSymlinks(command.TaskRoot)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-task-reply:", err)
		return 1
	}
	switch selected.Command() {
	case "send <task-id>":
		err = send(command, stdin, stdout)
	case "check <id>":
		err = check(command, stdout)
	default:
		fmt.Fprintln(stderr, "tincan-task-reply: unknown command")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "tincan-task-reply:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
