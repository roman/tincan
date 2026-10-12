package taskview

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/roman/tincan/pkg/stateversion"
	"github.com/roman/tincan/pkg/taskstate"
)

var ErrInvalidProjection = errors.New("invalid backlog projection path")
var ErrProjectionLockTimeout = errors.New("backlog projection lock timed out")

var ErrProjectionPublicationUnconfirmed = errors.New(
	"backlog projection publication requires reconciliation",
)

// WriteMarkdown renders a rebuildable human view from authoritative task records.
func WriteMarkdown(output io.Writer, tasks []taskstate.Task) error {
	if _, err := io.WriteString(output, "# Backlog\n\n"); err != nil {
		return err
	}
	for _, section := range []struct {
		name  string
		state taskstate.State
	}{
		{name: "In flight", state: taskstate.InFlight},
		{name: "Queued", state: taskstate.Queued},
		{name: "Done", state: taskstate.Done},
	} {
		if _, err := fmt.Fprintf(output, "## %s\n", section.name); err != nil {
			return err
		}
		for _, task := range tasks {
			if task.State() != section.state {
				continue
			}
			if err := writeMarkdownTask(output, task); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeMarkdownTask(output io.Writer, task taskstate.Task) error {
	definition := task.Definition()
	marker := "- [ ]"
	switch task.State() {
	case taskstate.InFlight:
		marker = "- **" + task.ID() + "**"
	case taskstate.Done:
		marker = "- [x]"
	}
	if task.State() == taskstate.InFlight {
		if _, err := fmt.Fprintf(output, "%s - %s", marker, definition.Title); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(output, "%s %s - %s", marker, task.ID(), definition.Title); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, " (repo: %s) (kind: %s)", definition.Repo, definition.Kind); err != nil {
		return err
	}
	if definition.Priority != nil {
		if _, err := fmt.Fprintf(output, " (priority: %d)", *definition.Priority); err != nil {
			return err
		}
	}
	if definition.Created != "" {
		if _, err := fmt.Fprintf(output, " (since %s)", definition.Created); err != nil {
			return err
		}
	}
	if definition.Closed != "" {
		if _, err := fmt.Fprintf(output, " (done %s)", definition.Closed); err != nil {
			return err
		}
	}
	if definition.Hold != nil {
		if _, err := fmt.Fprintf(output, " (hold: %s)", definition.Hold.Reason); err != nil {
			return err
		}
		if definition.Hold.Kind != "" {
			if _, err := fmt.Fprintf(output, " (hold-kind: %s)", definition.Hold.Kind); err != nil {
				return err
			}
		}
		if definition.Hold.Until != "" {
			if _, err := fmt.Fprintf(output, " (hold-until: %s)", definition.Hold.Until); err != nil {
				return err
			}
		}
	}
	for _, dependency := range definition.Dependencies {
		if _, err := fmt.Fprintf(output, " %s: %s", dependency.Type, dependency.ID); err != nil {
			return err
		}
		if dependency.Reason != "" {
			if _, err := fmt.Fprintf(output, " - %s", dependency.Reason); err != nil {
				return err
			}
		}
	}
	if _, err := io.WriteString(output, "\n"); err != nil {
		return err
	}
	if definition.Body == "" {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSuffix(definition.Body, "\n"), "\n") {
		if _, err := fmt.Fprintf(output, "  %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

// Rebuild atomically replaces backlog.md from version 2 task records.
func Rebuild(ctx context.Context, stateRoot string) error {
	return rebuildWithSync(ctx, stateRoot, syncProjectionDirectory)
}

func rebuildWithSync(
	ctx context.Context,
	stateRoot string,
	syncDirectory func(*os.Root) error,
) error {
	guard, err := stateversion.AcquireVersionTwo(ctx, stateRoot)
	if err != nil {
		return err
	}
	defer guard.Close()
	root := guard.StateRoot()
	return withProjectionLock(ctx, root, func() error {
		tasks, err := taskstate.ListPinned(ctx, root)
		if err != nil {
			return err
		}
		selected, err := root.Lstat("backlog.md")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && !selected.Mode().IsRegular() {
			return ErrInvalidProjection
		}
		name := ".backlog-view-" + rand.Text()
		temporary, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer root.Remove(name)
		if err := WriteMarkdown(temporary, tasks); err != nil {
			temporary.Close()
			return err
		}
		if err := temporary.Sync(); err != nil {
			temporary.Close()
			return err
		}
		if err := temporary.Close(); err != nil {
			return err
		}
		if err := root.Rename(name, "backlog.md"); err != nil {
			return err
		}
		if err := syncDirectory(root); err != nil {
			return errors.Join(ErrProjectionPublicationUnconfirmed, err)
		}
		return nil
	})
}

func syncProjectionDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func withProjectionLock(ctx context.Context, root *os.Root, operation func() error) error {
	const name = ".backlog-view.lock"
	lock, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	selected, err := root.Lstat(name)
	if err != nil {
		return err
	}
	opened, err := lock.Stat()
	if err != nil || !selected.Mode().IsRegular() || !os.SameFile(selected, opened) {
		return ErrInvalidProjection
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return ErrProjectionLockTimeout
		case <-ticker.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}
