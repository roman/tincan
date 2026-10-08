package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
)

type cli struct {
	State    string  `help:"Absolute Firstmate state directory." required:""`
	Actor    string  `help:"Supervision actor (main or branch)." env:"FM_SUPERVISION_ACTOR" default:"main"`
	TaskID   string  `arg:"" help:"Firstmate task ID."`
	Body     *string `help:"Message body."`
	BodyFile string  `help:"Read message body from file; use - for stdin."`
}

func bodyFor(command cli, stdin io.Reader) (string, int, error) {
	if command.Body != nil && command.BodyFile != "" {
		return "", 2, errors.New("choose either --body or --body-file")
	}
	if command.Body != nil {
		return *command.Body, 0, nil
	}
	if command.BodyFile == "" {
		return "", 2, errors.New("provide --body or --body-file")
	}
	var data []byte
	var err error
	if command.BodyFile == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(command.BodyFile)
	}
	if err != nil {
		return "", 1, err
	}
	return string(data), 0, nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var command cli
	parser, err := kong.New(
		&command,
		kong.Name("tincan-firstmate-send"),
		kong.Writers(stdout, stderr),
	)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send:", err)
		return 2
	}
	if _, err = parser.Parse(args); err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send:", err)
		return 2
	}
	body, code, err := bodyFor(command, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send:", err)
		return code
	}
	state, err := firstmate.ParseAbsolutePath(command.State)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send:", err)
		return 2
	}
	id, err := firstmate.ParseTaskID(command.TaskID)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send:", err)
		return 2
	}
	actor, err := firstmate.ParseLeaseActor(command.Actor)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send:", err)
		return 2
	}
	sender := firstmate.Sender{
		StateDir: state,
		TaskID:   id,
		Actor:    actor,
	}
	result, err := sender.Send(context.Background(), body)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send:", err)
		if errors.Is(err, firstmate.ErrLeaseConflict) {
			return 6
		}
		return 1
	}
	if result.CleanupErr != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send: cleanup:", result.CleanupErr)
	}
	fmt.Fprintf(
		stdout,
		"status: recorded\nrecord: %s\ndoorbell: %s\n",
		result.RecordPath,
		"deferred",
	)
	return 0
}
func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
