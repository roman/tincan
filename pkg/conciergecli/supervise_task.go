package conciergecli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/resident"
	"github.com/roman/tincan/pkg/supervise"
	"github.com/roman/tincan/pkg/taskops"
)

func (command *cli) runTaskSupervision(ctx context.Context, root string, output io.Writer) int {
	config, err := command.Supervise.config()
	if err != nil {
		return fail(output, 2, err, supervisionHelp)
	}
	messageRoot, err := rootWithDefault(command.MessageRoot, ".tincan")
	if err != nil {
		return fail(output, 1, err, "provide an absolute --message-root path")
	}
	if _, err := firstmate.ParseAbsolutePath(messageRoot); err != nil {
		return fail(output, 2, err, "use an absolute, clean --message-root path")
	}
	service, err := taskops.New(root)
	if err != nil {
		return fail(output, 2, err, supervisionHelp)
	}
	taskID := command.Supervise.Task.TaskID
	worker, err := service.SupervisedWorker(
		taskID,
		messageRoot,
		herdr.NewClient(command.Supervise.Herdr),
	)
	if err != nil {
		return fail(output, 2, err, supervisionHelp)
	}
	messages, err := inbox.New(messageRoot)
	if err != nil {
		return fail(output, 2, err, supervisionHelp)
	}
	if err := runTaskResident(ctx, root, taskID, worker, messages, config, output); err != nil {
		return fail(
			output,
			1,
			err,
			supervisionHelp+"; inspect the task and worker binding before restarting supervision",
		)
	}
	return 0
}

func runTaskResident(
	ctx context.Context,
	root, taskID string,
	worker *taskops.SupervisedWorker,
	messages *inbox.Store,
	config supervise.Config,
	output io.Writer,
) (runErr error) {
	if ctx.Err() != nil {
		return nil
	}
	lease, err := resident.AcquireTaskLease(root, taskID)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, lease.Close()) }()
	if err := supervise.Check(ctx, worker); err != nil && !errors.Is(err, supervise.ErrRestarted) {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	recipient, err := inbox.ParseRecipient(taskID)
	if err != nil {
		return err
	}
	childContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var reportError error
	controller := resident.Controller{
		Worker: worker, Supervision: config,
		Watcher: resident.Watcher{
			Store: messages, Recipient: recipient, Target: taskID, Doorbell: worker,
			Report: func(deliveryError error) {
				if _, err := fmt.Fprintf(output, "delivery_error: %s\n", quote(deliveryError.Error())); err != nil {
					reportError = err
					cancel()
				}
			},
		},
	}
	// Controller.Run joins its watcher before returning, including report writes.
	controllerError := controller.Run(childContext, nil)
	return errors.Join(controllerError, reportError)
}
