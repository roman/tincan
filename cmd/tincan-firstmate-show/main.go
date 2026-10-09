package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
)

type cli struct {
	State  string `help:"Absolute Firstmate task state directory." required:""`
	Full   bool   `help:"Show the complete task message body."`
	TaskID string `help:"Task ID."                                             arg:""`
	Record string `help:"Numbered message record."                             arg:""`
}

func quoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func run(args []string, stdout, stderr io.Writer) int {
	var command cli
	parser, err := kong.New(
		&command,
		kong.Name("tincan-firstmate-show"),
		kong.Description("Inspect one local Firstmate task and message without changing state."),
		kong.Writers(stdout, stderr),
	)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-show:", err)
		return 2
	}
	if _, err := parser.Parse(args); err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-show:", err)
		return 2
	}
	state, err := firstmate.ParseAbsolutePath(command.State)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-show:", err)
		return 2
	}
	id, err := firstmate.ParseTaskID(command.TaskID)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-show:", err)
		return 2
	}
	record, err := firstmate.ParseRecordName(command.Record)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-show:", err)
		return 2
	}
	metadata, err := firstmate.ReadTask(state, id)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-show:", err)
		return 1
	}
	var inspection firstmate.RecordInspection
	if command.Full {
		inspection, err = firstmate.InspectRecordFull(state, id, record)
	} else {
		inspection, err = firstmate.InspectRecord(state, id, record)
	}
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-show:", err)
		return 1
	}
	current, err := firstmate.ReadTask(state, id)
	if err != nil || current != metadata {
		fmt.Fprintln(stderr, "tincan-firstmate-show: task metadata changed during inspection")
		return 1
	}
	fmt.Fprintf(stdout, "task: %s\n", id)
	fmt.Fprintf(stdout, "project: %s\n", quoted(metadata.Project().String()))
	fmt.Fprintf(stdout, "worktree: %s\n", quoted(metadata.Worktree().String()))
	fmt.Fprintf(stdout, "window: %s\n", metadata.Window())
	fmt.Fprintf(stdout, "pane_id: %s\n", metadata.HerdrPaneID())
	fmt.Fprintf(stdout, "harness: %s\n", metadata.Harness())
	fmt.Fprintf(stdout, "kind: %s\n", metadata.Kind())
	fmt.Fprintf(stdout, "spawn_generation: %s\n", metadata.SpawnGeneration())
	fmt.Fprintf(stdout, "record: %s\n", record)
	fmt.Fprintf(stdout, "record_state: %s\n", inspection.State)
	fmt.Fprintf(stdout, "body_chars: %d\n", inspection.BodyCharacters)
	fmt.Fprintf(stdout, "body: %s\n", quoted(inspection.Body))
	fmt.Fprintf(stdout, "truncated: %t\n", inspection.Truncated)
	if inspection.Truncated {
		fmt.Fprintln(stdout, "help: rerun with --full to read the complete message body")
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
