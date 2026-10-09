package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

type cli struct {
	State        string        `help:"Absolute Firstmate state directory."                                      required:""`
	WakeRoot     string        `help:"Absolute Tincan inbox root for durable escalation wakes."                 required:""`
	Herdr        string        `help:"Herdr executable."                                                                    env:"TINCAN_HERDR" default:"herdr"`
	GraceSeconds *string       `help:"Seconds before and between rings (default 90; FM_TASK_INBOX_GRACE_SECS)."`
	MaxAttempts  *string       `help:"Ring budget (default 3; FM_TASK_INBOX_RING_MAX)."`
	Interval     time.Duration `help:"Time between sweeps."                                                                                    default:"5s"`
	Once         bool          `help:"Run one sweep and exit."`
}

type reportMode uint8

const (
	reportOnce reportMode = iota
	reportResident
)

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

func actionName(outcome firstmate.CheckOutcome) string {
	switch outcome {
	case firstmate.CheckQuiet:
		return "quiet"
	case firstmate.CheckWait:
		return "wait"
	case firstmate.CheckRung:
		return "ring"
	case firstmate.CheckEscalate, firstmate.CheckUnavailable:
		return "escalate"
	default:
		return "unknown"
	}
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

func printReports(stdout io.Writer, reports []firstmate.SweepReport, mode reportMode) int {
	activity := make([]firstmate.SweepReport, 0)
	failures := make([]firstmate.SweepReport, 0)
	for _, report := range reports {
		if report.Err != nil {
			failures = append(failures, report)
		} else if report.Result.Outcome != firstmate.CheckQuiet {
			activity = append(activity, report)
		}
	}
	if mode == reportOnce {
		fmt.Fprintf(stdout, "tasks: %d\n", len(reports))
	}
	if mode == reportOnce || len(activity) > 0 {
		fmt.Fprintf(stdout, "activity[%d]{task,action,record,delivery}:\n", len(activity))
	}
	for _, report := range activity {
		delivery := "none"
		if report.Result.Outcome == firstmate.CheckRung {
			delivery = deliveryName(report.Result.Delivery.Outcome)
			if report.Result.Delivery.Outcome == herdr.RingFailed {
				ringErr := report.Result.Delivery.Err
				if ringErr == nil {
					ringErr = fmt.Errorf("terminal ring failed")
				}
				failures = append(failures, firstmate.SweepReport{Task: report.Task, Err: ringErr})
			}
		}
		fmt.Fprintf(
			stdout, "  %s,%s,%s,%s\n",
			report.Task, actionName(report.Result.Outcome), report.Result.Record, delivery,
		)
	}
	if mode == reportOnce || len(failures) > 0 {
		fmt.Fprintf(stdout, "errors[%d]{task,message}:\n", len(failures))
	}
	for _, report := range failures {
		fmt.Fprintf(stdout, "  %s,%s\n", report.Task, strconv.Quote(report.Err.Error()))
	}
	if len(failures) > 0 {
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var command cli
	parser, err := kong.New(
		&command,
		kong.Name("tincan-firstmate-watch"),
		kong.Writers(stdout, stderr),
	)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-watch:", err)
		return 2
	}
	if _, err := parser.Parse(args); err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-watch:", err)
		return 2
	}
	state, err := firstmate.ParseAbsolutePath(command.State)
	if err != nil || !filepath.IsAbs(command.WakeRoot) {
		fmt.Fprintln(stderr, "tincan-firstmate-watch: state and wake root must be absolute")
		return 2
	}
	if command.Interval <= 0 {
		fmt.Fprintln(stderr, "tincan-firstmate-watch: interval must be positive")
		return 2
	}
	grace, err := policyNumber(command.GraceSeconds, "FM_TASK_INBOX_GRACE_SECS")
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-watch:", err)
		return 2
	}
	maximum, err := policyNumber(command.MaxAttempts, "FM_TASK_INBOX_RING_MAX")
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-watch:", err)
		return 2
	}
	publisher, err := firstmate.NewInboxPublisher(command.WakeRoot)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-watch:", err)
		return 2
	}
	writer, err := firstmate.AcquireRetryWriter(state)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-firstmate-watch:", err)
		return 1
	}
	defer writer.Release()
	state = writer.StateDir
	endpoint := herdr.NewClient(command.Herdr)
	policy := firstmate.NewRetryPolicy(grace, maximum)
	check := func(ctx context.Context, id firstmate.TaskID, now time.Time) (firstmate.CheckResult, error) {
		store, err := firstmate.NewRetryStore(state, id, policy)
		if err != nil {
			return firstmate.CheckResult{}, err
		}
		return (firstmate.RetryCheck{
			StateDir: state,
			TaskID:   id,
			Store:    store,
			Health:   endpoint,
			Doorbell: endpoint,
			Publish:  publisher,
		}).Run(ctx, now)
	}
	for {
		reports, err := firstmate.Sweep(ctx, state, time.Now(), check)
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			fmt.Fprintln(stderr, "tincan-firstmate-watch:", err)
			return 1
		}
		mode := reportResident
		if command.Once {
			mode = reportOnce
		}
		code := printReports(stdout, reports, mode)
		if command.Once {
			return code
		}
		timer := time.NewTimer(command.Interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return 0
		case <-timer.C:
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
