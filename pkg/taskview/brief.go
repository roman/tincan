package taskview

import (
	"fmt"
	"io"
	"strings"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

// BriefOptions supplies presentation values without filesystem discovery.
type BriefOptions struct {
	Limit       int
	Recipient   inbox.Recipient
	Root        string
	MessageRoot string
	Executable  string
}

// WriteBrief renders bounded sections from the caller's current task and message reads.
func WriteBrief(
	output io.Writer,
	tasks []taskstate.Task,
	wakes []inbox.Message,
	options BriefOptions,
) error {
	if options.Limit < 1 || options.Limit > 100 {
		return fmt.Errorf("brief limit must be from 1 to 100")
	}
	if _, err := inbox.ParseRecipient(options.Recipient.String()); err != nil {
		return err
	}
	workers, adoptions := workerTasks(tasks)
	completions := completionTasks(tasks)
	var queued []taskstate.Task
	for _, task := range tasks {
		if task.State() == taskstate.Queued && task.Definition().Hold == nil {
			queued = append(queued, task)
		}
	}
	wakeCount := min(len(wakes), options.Limit)
	queueCount := min(len(queued), options.Limit)
	workerCount := min(len(workers), options.Limit)
	adoptionCount := min(len(adoptions), options.Limit)
	completionCount := min(len(completions), options.Limit)
	// Building in memory lets every section share one checked output operation.
	var view strings.Builder
	fmt.Fprintf(
		&view,
		"bin: %s\ndescription: %s\n",
		quote(options.Executable),
		quote("Pending primary wakes, queued backlog work, local tasks, and registered workers"),
	)
	writeBriefCount(
		&view,
		"wakes",
		wakeCount,
		len(wakes),
		"pending for "+options.Recipient.String(),
	)
	if wakeCount > 0 {
		fmt.Fprintf(&view, "wakes[%d]{id,body}:\n", wakeCount)
		for _, wake := range wakes[:wakeCount] {
			fmt.Fprintf(&view, "  %s,%s\n", quote(wake.ID.String()), quote(wakePreview(wake.Body)))
		}
	}
	writeBriefCount(&view, "backlog", queueCount, len(queued), "queued")
	if queueCount > 0 {
		fmt.Fprintf(&view, "backlog[%d]{id,state,kind,repo,title}:\n", queueCount)
		for _, task := range queued[:queueCount] {
			definition := task.Definition()
			fmt.Fprintf(
				&view,
				"  %s,%s,%s,%s,%s\n",
				quote(task.ID()),
				task.State(),
				quote(definition.Kind),
				quote(definition.Repo),
				quote(definition.Title),
			)
		}
	}
	writeBriefCount(&view, "tasks", workerCount, len(workers), "found")
	if workerCount > 0 {
		_ = writeLocalTaskRows(&view, workers[:workerCount])
	}
	writeBriefCount(&view, "workers", workerCount, len(workers), "registered")
	if workerCount > 0 {
		_ = writeWorkerRows(&view, "workers", workers[:workerCount])
	}
	if adoptionCount > 0 {
		fmt.Fprintf(&view, "pending_adoptions: %d of %d\n", adoptionCount, len(adoptions))
		_ = writeWorkerRows(&view, "pending_adoptions", adoptions[:adoptionCount])
	}
	if completionCount > 0 {
		fmt.Fprintf(&view, "pending_completions: %d of %d\n", completionCount, len(completions))
		_ = writeCompletionRows(&view, completions[:completionCount])
	}
	supervisor := "concierge --root " + shellQuote(options.Root)
	transport := "tincan --root " + shellQuote(options.MessageRoot) + " inbox"
	var hints []string
	if wakeCount > 0 {
		hints = append(hints, transport+" read "+options.Recipient.String()+" <id>")
	}
	if wakeCount < len(wakes) {
		hints = append(hints, transport+" list "+options.Recipient.String())
	}
	if queueCount < len(queued) {
		hints = append(hints, supervisor+" backlog list")
	}
	if workerCount > 0 {
		hints = append(hints, supervisor+" task list")
	}
	if workerCount > 0 || adoptionCount > 0 {
		hints = append(hints, supervisor+" worker show <task-id>")
	}
	if workerCount < len(workers) || adoptionCount < len(adoptions) {
		hints = append(hints, supervisor+" worker list")
	}
	if completionCount > 0 {
		if completionCount < len(completions) {
			hints = append(hints, supervisor+" task pending")
		}
		hints = append(
			hints,
			supervisor+" backlog finish --help",
			supervisor+" task stop --help",
			supervisor+" backlog cleanup --help",
		)
	}
	if len(hints) > 0 {
		for index, hint := range hints {
			hints[index] = quote(hint)
		}
		fmt.Fprintf(&view, "help[%d]: %s\n", len(hints), strings.Join(hints, ","))
	}
	_, err := io.WriteString(output, view.String())
	return err
}

func writeBriefCount(output *strings.Builder, name string, shown, total int, suffix string) {
	if shown == total {
		fmt.Fprintf(output, "%s: %d %s\n", name, shown, suffix)
		return
	}
	fmt.Fprintf(output, "%s: %d of %d %s\n", name, shown, total, suffix)
}

func wakePreview(body string) string {
	const maximumRunes = 120
	characters := []rune(body)
	if len(characters) <= maximumRunes {
		return body
	}
	return fmt.Sprintf("%s… (%d chars total)", string(characters[:maximumRunes]), len(characters))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
