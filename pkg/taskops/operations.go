// Package taskops connects version 2 task records to Concierge backlog operations.
package taskops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/taskview"
)

var ErrBlockedDependency = errors.New("task has an unfinished dependency")

const DefaultPrimaryRecipient = "concierge"

// Service updates authoritative task records and rebuilds their human view.
type Service struct {
	root             string
	store            *taskstate.Store
	primaryRecipient inbox.Recipient
}

// New prepares backlog operations for the selected task root.
func New(root string) (*Service, error) {
	recipient, _ := inbox.ParseRecipient(DefaultPrimaryRecipient)
	return NewWithPrimaryRecipient(root, recipient)
}

// NewWithPrimaryRecipient directs supervisor messages to one selected mailbox.
func NewWithPrimaryRecipient(root string, recipient inbox.Recipient) (*Service, error) {
	if _, err := inbox.ParseRecipient(recipient.String()); err != nil {
		return nil, err
	}
	store, err := taskstate.NewStore(root)
	if err != nil {
		return nil, err
	}
	return &Service{root: root, store: store, primaryRecipient: recipient}, nil
}

// Create adds a queued task and refreshes the backlog view.
func (service *Service) Create(
	ctx context.Context,
	definition taskstate.Definition,
) (taskstate.Task, error) {
	task, err := taskstate.NewQueued(definition)
	if err != nil {
		return taskstate.Task{}, err
	}
	if err := service.store.Create(ctx, task); err != nil {
		if errors.Is(err, os.ErrExist) {
			current, readErr := service.store.Read(ctx, task.ID())
			if readErr != nil {
				return taskstate.Task{}, readErr
			}
			currentData, encodeErr := current.Encode()
			if encodeErr != nil {
				return taskstate.Task{}, encodeErr
			}
			requestedData, encodeErr := task.Encode()
			if encodeErr != nil {
				return taskstate.Task{}, encodeErr
			}
			if !bytes.Equal(currentData, requestedData) {
				return taskstate.Task{}, err
			}
		} else if !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
			return taskstate.Task{}, err
		} else {
			return task, errors.Join(err, taskview.Rebuild(ctx, service.root))
		}
	}
	return task, taskview.Rebuild(ctx, service.root)
}

// Start dispatches a bound task once its blocking dependencies are done.
func (service *Service) Start(ctx context.Context, taskID string) (taskstate.Task, error) {
	tasks, err := service.store.List(ctx)
	if err != nil {
		return taskstate.Task{}, err
	}
	var selected taskstate.Task
	states := make(map[string]taskstate.State, len(tasks))
	for _, task := range tasks {
		states[task.ID()] = task.State()
		if task.ID() == taskID {
			selected = task
		}
	}
	if selected.ID() == "" {
		return taskstate.Task{}, fmt.Errorf("task %q is missing", taskID)
	}
	for _, dependency := range selected.Definition().Dependencies {
		if dependency.Type == "blocked-by" && states[dependency.ID] != taskstate.Done {
			return taskstate.Task{}, fmt.Errorf("%w: %s", ErrBlockedDependency, dependency.ID)
		}
	}
	updated, err := service.store.Update(ctx, taskID, taskstate.Task.Start)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}

// Finish accepts one worker generation and updates the backlog view.
func (service *Service) Finish(
	ctx context.Context, taskID, generation string, mode taskstate.CompletionMode, closedOn string,
) (taskstate.Task, error) {
	updated, err := service.store.Update(
		ctx,
		taskID,
		func(task taskstate.Task) (taskstate.Task, error) {
			return task.AcceptResult(generation, mode, closedOn)
		},
	)
	if err != nil && !errors.Is(err, taskstate.ErrPublicationUnconfirmed) {
		return taskstate.Task{}, err
	}
	return updated, errors.Join(err, taskview.Rebuild(ctx, service.root))
}
