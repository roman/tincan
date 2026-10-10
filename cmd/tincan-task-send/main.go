package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
)

type cli struct {
	TaskRoot    string `help:"Absolute Concierge state root." required:""`
	MessageRoot string `help:"Absolute Tincan message root." required:""`
	TaskID      string `arg:"" help:"Bound local task ID."`
	Key         string `help:"Stable key for retry-safe sends."`
	BodyFile    string `help:"Read message body from a file; use - for stdin." required:""`
}

type sendResult struct {
	message      inbox.Message
	recorded     bool
	notification string
	notifyErr    error
}

func messageBody(path string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	return string(data), err
}

func notifyWorker(
	ctx context.Context, task *crew.CurrentTask, client herdr.Client,
) error {
	record := task.Record()
	bound, err := client.ForSession(record.HerdrSession)
	if err != nil {
		return err
	}
	agent, err := bound.Get(ctx, record.PaneID)
	if err != nil {
		return err
	}
	if agent.PaneID != record.PaneID || agent.Kind != record.Kind {
		return fmt.Errorf("worker identity differs from task %s", record.TaskID)
	}
	directory, err := bound.ForegroundDirectory(ctx, record.PaneID)
	if err != nil {
		return err
	}
	worktree, err := os.Stat(record.Worktree)
	if err != nil {
		return err
	}
	foreground, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !worktree.IsDir() || !foreground.IsDir() || !os.SameFile(worktree, foreground) {
		return fmt.Errorf("worker pane is outside task worktree")
	}
	if err := task.CheckCurrent(); err != nil {
		return err
	}
	return bound.Prompt(ctx, record.PaneID, notify.DoorbellText)
}

func send(
	ctx context.Context, options cli, body string, client herdr.Client,
) (sendResult, error) {
	registry, err := crew.New(options.TaskRoot)
	if err != nil {
		return sendResult{}, err
	}
	store, err := inbox.New(options.MessageRoot)
	if err != nil {
		return sendResult{}, err
	}
	recipient, err := inbox.ParseRecipient(options.TaskID)
	if err != nil {
		return sendResult{}, err
	}
	var result sendResult
	err = registry.WithCurrentTask(options.TaskID, func(task *crew.CurrentTask) error {
		message, err := store.Send(recipient, options.Key, body)
		if err != nil {
			return err
		}
		result = sendResult{message: message, recorded: true, notification: "deferred"}
		if err := task.CheckCurrent(); err != nil {
			result.notifyErr = err
			return nil
		}
		pending, err := store.List(recipient)
		if err != nil {
			result.notifyErr = err
			return nil
		}
		if len(pending) == 0 {
			result.notification = "acknowledged"
			return nil
		}
		if err := notifyWorker(ctx, task, client); err != nil {
			result.notification = "failed"
			result.notifyErr = err
			return nil
		}
		result.notification = "sent"
		return nil
	})
	return result, err
}

func run(
	ctx context.Context, arguments []string, stdin io.Reader,
	stdout, stderr io.Writer, client herdr.Client,
) int {
	var options cli
	parser, err := kong.New(&options, kong.Name("tincan-task-send"),
		kong.Writers(stdout, stderr))
	if err != nil {
		fmt.Fprintln(stderr, "tincan-task-send:", err)
		return 2
	}
	if _, err := parser.Parse(arguments); err != nil {
		fmt.Fprintln(stderr, "tincan-task-send:", err)
		return 2
	}
	if _, err := firstmate.ParseTaskID(options.TaskID); err != nil {
		fmt.Fprintln(stderr, "tincan-task-send:", err)
		return 2
	}
	if !filepath.IsAbs(options.TaskRoot) || !filepath.IsAbs(options.MessageRoot) {
		fmt.Fprintln(stderr, "tincan-task-send: state roots must be absolute")
		return 2
	}
	body, err := messageBody(options.BodyFile, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "tincan-task-send:", err)
		return 1
	}
	result, err := send(ctx, options, body, client)
	if !result.recorded {
		fmt.Fprintln(stderr, "tincan-task-send:", err)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "tincan-task-send: cleanup:", err)
	}
	if result.notifyErr != nil {
		fmt.Fprintln(stderr, "tincan-task-send: notification:", result.notifyErr)
	}
	fmt.Fprintf(stdout, "status: recorded\nid: %s\nrecipient: %s\nnotification: %s\n",
		result.message.ID, result.message.Recipient, result.notification)
	return 0
}

func main() {
	ctx := context.Background()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		herdr.NewClient("")))
}
