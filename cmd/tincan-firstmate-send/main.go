package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

type cli struct {
	State    string  `help:"Absolute Firstmate state directory." required:""`
	Actor    string  `help:"Supervision actor (main or branch)." env:"FM_SUPERVISION_ACTOR" default:"main"`
	Herdr    string  `help:"Herdr executable." env:"TINCAN_HERDR" default:"herdr"`
	TaskID   string  `arg:"" help:"Firstmate task ID."`
	Body     *string `help:"Message body."`
	BodyFile string  `help:"Read message body from file; use - for stdin."`
}

type failedDoorbell struct{ err error }

func (doorbell failedDoorbell) Ring(context.Context, herdr.Target, string) herdr.RingResult {
	return herdr.RingResult{Outcome: herdr.RingFailed, Err: doorbell.err}
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
	var terminalDoorbell firstmate.Doorbell = herdr.NewClient(command.Herdr)
	if _, err := exec.LookPath(command.Herdr); err != nil {
		terminalDoorbell = failedDoorbell{
			err: fmt.Errorf("Herdr executable %q: %w", command.Herdr, err),
		}
	}
	sender := firstmate.Sender{
		StateDir: state,
		TaskID:   id,
		Actor:    actor,
		Doorbell: terminalDoorbell,
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
	doorbell := "deferred"
	switch result.Doorbell.Outcome {
	case herdr.RingSent:
		doorbell = "sent"
	case herdr.RingFailed:
		doorbell = "failed"
	case herdr.RingUnavailable:
		doorbell = "unavailable"
	case herdr.RingSkipped, herdr.RingNotAttempted:
	default:
		doorbell = "failed"
	}
	if result.Doorbell.Err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-send: doorbell:", result.Doorbell.Err)
	} else if doorbell == "failed" || doorbell == "unavailable" {
		fmt.Fprintln(stderr, "tincan-firstmate-send: doorbell:", doorbell)
	}
	fmt.Fprintf(
		stdout,
		"status: recorded\nrecord: %s\ndoorbell: %s\n",
		result.RecordPath,
		doorbell,
	)
	return 0
}
func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
