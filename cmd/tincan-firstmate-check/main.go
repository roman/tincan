package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

type cli struct {
	State        string  `help:"Absolute Firstmate state directory." required:""`
	Herdr        string  `help:"Herdr executable." env:"TINCAN_HERDR" default:"herdr"`
	GraceSeconds *string `help:"Seconds before and between watcher rings (default 90; FM_TASK_INBOX_GRACE_SECS)."`
	MaxAttempts  *string `help:"Watcher ring budget (default 3; FM_TASK_INBOX_RING_MAX)."`
	TaskID       string  `arg:"" help:"Firstmate task ID."`
}

type failedHerdr struct{ err error }

func (adapter failedHerdr) Health(context.Context, herdr.Target) herdr.HealthStatus {
	return herdr.HealthStatus{Endpoint: herdr.Unreadable, Activity: herdr.HealthUnknown}
}

func (adapter failedHerdr) Ring(context.Context, herdr.Target, string) herdr.RingResult {
	return herdr.RingResult{Outcome: herdr.RingFailed, Err: adapter.err}
}

func policyNumber(explicit *string, envName string) (string, error) {
	if explicit == nil {
		return os.Getenv(envName), nil
	}
	value := *explicit
	if value == "" || strings.Trim(value, "0123456789") != "" {
		return "", fmt.Errorf("invalid %s value %q", envName, value)
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return "", fmt.Errorf("invalid %s value %q: %w", envName, value, err)
	}
	return value, nil
}

func deliveryName(outcome herdr.RingOutcome) string {
	switch outcome {
	case herdr.RingSent:
		return "sent"
	case herdr.RingSkipped, herdr.RingNotAttempted:
		return "deferred"
	case herdr.RingFailed:
		return "failed"
	case herdr.RingUnavailable:
		return "unavailable"
	default:
		return "failed"
	}
}

type checkExecutor func(firstmate.RetryCheck, time.Time) (firstmate.CheckResult, error)

func run(args []string, stdout, stderr io.Writer) int {
	return runWithCheck(
		args,
		stdout,
		stderr,
		func(check firstmate.RetryCheck, now time.Time) (firstmate.CheckResult, error) {
			return check.Run(context.Background(), now)
		},
	)
}

func runWithCheck(args []string, stdout, stderr io.Writer, execute checkExecutor) int {
	var command cli
	parser, err := kong.New(
		&command,
		kong.Name("tincan-firstmate-check"),
		kong.Writers(stdout, stderr),
	)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-check:", err)
		return 2
	}
	if _, err = parser.Parse(args); err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-check:", err)
		return 2
	}
	state, err := firstmate.ParseAbsolutePath(command.State)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-check:", err)
		return 2
	}
	id, err := firstmate.ParseTaskID(command.TaskID)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-check:", err)
		return 2
	}
	grace, err := policyNumber(command.GraceSeconds, "FM_TASK_INBOX_GRACE_SECS")
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-check:", err)
		return 2
	}
	maximum, err := policyNumber(command.MaxAttempts, "FM_TASK_INBOX_RING_MAX")
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-check:", err)
		return 2
	}
	store, err := firstmate.NewRetryStore(state, id, firstmate.NewRetryPolicy(grace, maximum))
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-check:", err)
		return 1
	}
	var endpoint interface {
		firstmate.RetryHealth
		firstmate.Doorbell
	} = herdr.NewClient(command.Herdr)
	if _, err := exec.LookPath(command.Herdr); err != nil {
		endpoint = failedHerdr{err: fmt.Errorf("Herdr executable %q: %w", command.Herdr, err)}
	}
	check := firstmate.RetryCheck{
		StateDir: state,
		TaskID:   id,
		Store:    store,
		Health:   endpoint,
		Doorbell: endpoint,
	}
	now := time.Now()
	result, err := execute(check, now)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-check:", err)
		return 1
	}
	switch result.Outcome {
	case firstmate.CheckQuiet:
		fmt.Fprintln(stdout, "action: quiet")
	case firstmate.CheckWait:
		fmt.Fprintf(stdout, "action: wait\nrecord: %s\n", result.Record)
	case firstmate.CheckRung:
		fmt.Fprintf(
			stdout,
			"action: ring\nrecord: %s\nattempts: %d\ndelivery: %s\n",
			result.Record,
			result.Attempts,
			deliveryName(result.Delivery.Outcome),
		)
		if result.Delivery.Err != nil {
			fmt.Fprintln(stderr, "tincan-firstmate-check: doorbell:", result.Delivery.Err)
		}
	case firstmate.CheckEscalate, firstmate.CheckUnavailable:
		reason := "budget"
		if result.Outcome == firstmate.CheckUnavailable {
			reason = "unavailable"
		}
		fmt.Fprintf(
			stdout,
			"action: escalate\nreason: %s\nrecord: %s\nattempts: %d\n",
			reason,
			result.Record,
			result.Attempts,
		)
	default:
		fmt.Fprintln(stderr, "tincan-firstmate-check: unknown check outcome")
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
