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
)

type cli struct {
	TaskRoot    string       `help:"Absolute Concierge state root." required:""`
	MessageRoot string       `help:"Absolute Tincan message root." required:""`
	Send        sendCommand  `cmd:"" help:"Send a reply bound to the current worker generation."`
	Check       checkCommand `cmd:"" help:"Check a pending reply against the current worker generation."`
}

type sendCommand struct {
	TaskID     string `arg:"" help:"Bound local task ID."`
	Generation string `help:"Worker generation from the assignment." required:""`
	Key        string `help:"Stable key for retry-safe sends." required:""`
	BodyFile   string `help:"Read reply body from a file; use - for stdin." required:""`
}

type checkCommand struct {
	ID string `arg:"" help:"Pending Tincan message ID."`
}

type replyEnvelope struct {
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Task            string `json:"task"`
	SpawnGeneration string `json:"spawn_generation"`
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
	err = registry.WithCurrentTask(input.TaskID, func(task *crew.CurrentTask) error {
		if task.Record().SpawnGeneration != input.Generation {
			return errOldGeneration
		}
		envelope, err := json.Marshal(replyEnvelope{
			Kind: "task-reply", State: filepath.Join(command.TaskRoot, "tasks"),
			Task: input.TaskID, SpawnGeneration: input.Generation, Body: body,
		})
		if err != nil {
			return err
		}
		key := fmt.Sprintf("task-reply:%s:%s:%s:%s",
			command.TaskRoot, input.TaskID, input.Generation, input.Key)
		message, err = store.Send(recipient, key, string(envelope))
		return err
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
	registry, err := crew.New(command.TaskRoot)
	if err != nil {
		return err
	}
	err = registry.WithCurrentTask(envelope.Task, func(task *crew.CurrentTask) error {
		if task.Record().SpawnGeneration != envelope.SpawnGeneration {
			return errOldGeneration
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
