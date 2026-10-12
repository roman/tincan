// Package taskview renders version 2 task records for local Concierge commands.
package taskview

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/roman/tincan/pkg/taskstate"
)

// DetailLevel selects the amount of task text in a human view.
type DetailLevel int

const (
	DetailSummary DetailLevel = iota
	DetailFull
)

// WriteList renders tasks from records in their supplied order.
func WriteList(output io.Writer, tasks []taskstate.Task, state string) error {
	selected := make([]taskstate.Task, 0, len(tasks))
	for _, task := range tasks {
		definition := task.Definition()
		switch state {
		case "held":
			if task.State() != taskstate.Queued || definition.Hold == nil {
				continue
			}
		case "queued":
			if task.State() != taskstate.Queued || definition.Hold != nil {
				continue
			}
		default:
			if string(task.State()) != state {
				continue
			}
		}
		selected = append(selected, task)
	}
	if _, err := fmt.Fprintf(output, "count: %d\n", len(selected)); err != nil {
		return err
	}
	if len(selected) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(output, "tasks[%d]{id,state,kind,repo,title}:\n", len(selected)); err != nil {
		return err
	}
	for _, task := range selected {
		definition := task.Definition()
		if _, err := fmt.Fprintf(output, "  %s,%s,%s,%s,%s\n",
			quote(task.ID()), task.State(), quote(definition.Kind), quote(definition.Repo),
			quote(definition.Title)); err != nil {
			return err
		}
	}
	return nil
}

// WriteTask renders the complete task fields needed for supervision.
func WriteTask(output io.Writer, task taskstate.Task, detail DetailLevel) error {
	definition := task.Definition()
	title, body := definition.Title, definition.Body
	if detail == DetailSummary {
		title = summarize(title, 80)
		body = summarize(body, 200)
	}
	holdReason, holdKind, holdUntil := "-", "-", "-"
	held := "no"
	if definition.Hold != nil {
		held = "yes"
		holdReason = definition.Hold.Reason
		if definition.Hold.Kind != "" {
			holdKind = definition.Hold.Kind
		}
		if definition.Hold.Until != "" {
			holdUntil = definition.Hold.Until
		}
	}
	priority := "-"
	if definition.Priority != nil {
		priority = strconv.Itoa(*definition.Priority)
	}
	created, closed := definition.Created, definition.Closed
	if created == "" {
		created = "-"
	}
	if closed == "" {
		closed = "-"
	}
	blockedBy := make([]string, 0)
	deps := make([]string, 0, len(definition.Dependencies))
	for _, dependency := range definition.Dependencies {
		deps = append(deps, dependency.Type+":"+dependency.ID)
		if dependency.Type == "blocked-by" {
			blockedBy = append(blockedBy, dependency.ID)
		}
	}
	blocked := "no"
	if len(blockedBy) > 0 {
		blocked = "yes"
	}
	if len(deps) == 0 {
		deps = append(deps, "none")
	}
	if len(blockedBy) == 0 {
		blockedBy = append(blockedBy, "none")
	}
	_, err := fmt.Fprintf(output,
		"task:\n  id: %s\n  title: %s\n  state: %s\n  blocked: %s\n"+
			"  blocked_by: %s\n  held: %s\n  hold_reason: %s\n  hold_kind: %s\n"+
			"  hold_until: %s\n  kind: %s\n  repo: %s\n  priority: %s\n"+
			"  created: %s\n  closed: %s\n  deps: %s\n  body: %s\n",
		quote(task.ID()), quote(title), task.State(), blocked, quote(strings.Join(blockedBy, ",")),
		held, quote(holdReason), quote(holdKind), quote(holdUntil),
		quote(definition.Kind), quote(definition.Repo), priority,
		created, closed, quote(strings.Join(deps, ",")), quote(body),
	)
	return err
}

func summarize(input string, limit int) string {
	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	return string(runes[:limit]) + "\n... (truncated, " + strconv.Itoa(len(runes)) +
		" chars total - use --full to see complete text)"
}

func quote(input string) string {
	encoded, _ := json.Marshal(input)
	return string(encoded)
}
