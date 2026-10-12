package conciergecli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
)

func runRecordView(ctx context.Context, root, selected, taskID string, output io.Writer) int {
	store, err := taskstate.NewStore(root)
	if err != nil {
		return fail(output, 2, err, cliHelp)
	}
	if selected == "worker show <task-id>" {
		task, err := store.Read(ctx, taskID)
		if err != nil {
			return fail(output, 1, err, "inspect the task ID and initialized version 2 --root")
		}
		if err := taskview.WriteWorker(output, task); err != nil {
			return fail(output, 1, err, "run concierge worker list to inspect current bindings")
		}
		return 0
	}
	tasks, err := store.List(ctx)
	if err != nil {
		return fail(
			output,
			1,
			err,
			"provide an initialized version 2 --root and inspect its task records",
		)
	}
	switch selected {
	case "worker list":
		err = taskview.WriteWorkers(output, tasks)
	case "task list":
		err = taskview.WriteLocalTasks(output, tasks)
	case "task pending":
		err = taskview.WritePendingCompletions(output, tasks)
	}
	if err != nil {
		return 1
	}
	return 0
}

func (command *cli) runBrief(ctx context.Context, root string, output io.Writer) int {
	if command.Brief.Limit < 1 || command.Brief.Limit > 100 {
		return fail(
			output,
			2,
			fmt.Errorf("--limit must be from 1 to 100"),
			"run concierge brief --limit N with N from 1 to 100",
		)
	}
	recipient, err := inbox.ParseRecipient(command.Recipient)
	if err != nil {
		return fail(output, 2, err, "use a valid primary --recipient name")
	}
	messageRoot := command.MessageRoot
	if messageRoot == "" {
		directory, err := os.Getwd()
		if err != nil {
			return fail(output, 1, err, "provide an absolute --message-root path")
		}
		messageRoot = filepath.Join(directory, ".tincan")
	}
	if _, err := firstmate.ParseAbsolutePath(messageRoot); err != nil {
		return fail(output, 2, err, "use an absolute, clean --message-root path")
	}
	store, err := taskstate.NewStore(root)
	if err != nil {
		return fail(output, 2, err, cliHelp)
	}
	tasks, err := store.List(ctx)
	if err != nil {
		return fail(
			output,
			1,
			err,
			"provide an initialized version 2 --root and inspect its task records",
		)
	}
	messages, err := inbox.New(messageRoot)
	if err != nil {
		return fail(output, 2, err, "use an absolute, clean --message-root path")
	}
	wakes, err := messages.List(recipient)
	if err != nil {
		return fail(output, 1, err, "inspect the selected message root and primary inbox")
	}
	executable, err := os.Executable()
	if err != nil {
		return fail(output, 1, err, "check the Concierge executable path")
	}
	if err := taskview.WriteBrief(output, tasks, wakes, taskview.BriefOptions{Limit: command.Brief.Limit, Recipient: recipient, Root: root, MessageRoot: messageRoot, Executable: executable}); err != nil {
		return 1
	}
	return 0
}
