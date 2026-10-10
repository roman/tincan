package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/inbox"
)

type cli struct {
	Root        string `help:"Absolute Concierge state root; defaults to .concierge in the current directory." env:"CONCIERGE_HOME"`
	MessageRoot string `help:"Absolute Tincan message root; defaults to .tincan in the current directory." env:"TINCAN_HOME"`
	Recipient   string `help:"Primary inbox recipient." default:"firstmate"`
	Limit       int    `help:"Maximum rows from each source." default:"10"`
}

const version = "0.1.0"

func quoted(value string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(buffer.String(), "\n")
}

func failure(output io.Writer, code int, err error, help string) int {
	fmt.Fprintf(output, "error: %s\nhelp: %s\n", quoted(err.Error()), quoted(help))
	return code
}

func preview(body string) string {
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

func printBrief(
	output io.Writer,
	root, messageRoot string,
	recipient inbox.Recipient,
	limit int,
) error {
	registry, err := crew.New(root)
	if err != nil {
		return err
	}
	store, err := inbox.New(messageRoot)
	if err != nil {
		return err
	}
	wakes, err := store.List(recipient)
	if err != nil {
		return err
	}
	allRecords, err := registry.List()
	if err != nil {
		return err
	}
	taskState, err := firstmate.ParseAbsolutePath(filepath.Join(root, "tasks"))
	if err != nil {
		return err
	}
	tasks, err := firstmate.ListTasks(taskState)
	if err != nil {
		return err
	}
	backlog, err := readQueuedBacklog(root, limit)
	if err != nil {
		return err
	}
	completions, err := registry.ListPendingCompletions()
	if err != nil {
		return err
	}
	var workers, pending []crew.Record
	for _, record := range allRecords {
		if record.PendingAdoption {
			pending = append(pending, record)
			continue
		}
		workers = append(workers, record)
	}
	wakeCount := min(len(wakes), limit)
	taskCount := min(len(tasks), limit)
	workerCount := min(len(workers), limit)
	pendingCount := min(len(pending), limit)
	completionCount := min(len(completions), limit)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "bin: %s\n", quoted(executable))
	fmt.Fprintf(
		output,
		"description: %s\n",
		quoted("Pending primary wakes, queued backlog work, local tasks, and registered workers"),
	)
	if wakeCount == len(wakes) {
		fmt.Fprintf(output, "wakes: %d pending for %s\n", wakeCount, recipient)
	} else {
		fmt.Fprintf(output, "wakes: %d of %d pending for %s\n", wakeCount, len(wakes), recipient)
	}
	if wakeCount > 0 {
		fmt.Fprintf(output, "wakes[%d]{id,body}:\n", wakeCount)
		for _, wake := range wakes[:wakeCount] {
			fmt.Fprintf(output, "  %s,%s\n", quoted(wake.ID.String()), quoted(preview(wake.Body)))
		}
	}
	if len(backlog.rows) == backlog.total {
		fmt.Fprintf(output, "backlog: %d queued\n", backlog.total)
	} else {
		fmt.Fprintf(output, "backlog: %d of %d queued\n", len(backlog.rows), backlog.total)
	}
	if len(backlog.rows) > 0 {
		fmt.Fprintf(output, "backlog[%d]{id,state,kind,repo,title}:\n", len(backlog.rows))
		for _, row := range backlog.rows {
			fmt.Fprintln(output, row)
		}
	}
	if taskCount == len(tasks) {
		fmt.Fprintf(output, "tasks: %d found\n", taskCount)
	} else {
		fmt.Fprintf(output, "tasks: %d of %d found\n", taskCount, len(tasks))
	}
	if taskCount > 0 {
		fmt.Fprintf(output, "tasks[%d]{task,kind,worktree}:\n", taskCount)
		for _, task := range tasks[:taskCount] {
			fmt.Fprintf(output, "  %s,%s,%s\n", quoted(task.TaskID().String()),
				quoted(task.Kind()), quoted(task.Worktree().String()))
		}
	}
	if workerCount == len(workers) {
		fmt.Fprintf(output, "workers: %d registered\n", workerCount)
	} else {
		fmt.Fprintf(output, "workers: %d of %d registered\n", workerCount, len(workers))
	}
	if workerCount > 0 {
		fmt.Fprintf(output, "workers[%d]{task,name,session,pane_id}:\n", workerCount)
		for _, worker := range workers[:workerCount] {
			fmt.Fprintf(output, "  %s,%s,%s,%s\n", quoted(worker.TaskID), quoted(worker.Name),
				quoted(worker.HerdrSession), quoted(worker.PaneID))
		}
	}
	if pendingCount > 0 {
		fmt.Fprintf(output, "pending_adoptions: %d of %d\n", pendingCount, len(pending))
		fmt.Fprintf(output, "pending_adoptions[%d]{task,name,session,pane_id}:\n", pendingCount)
		for _, record := range pending[:pendingCount] {
			fmt.Fprintf(output, "  %s,%s,%s,%s\n", quoted(record.TaskID), quoted(record.Name),
				quoted(record.HerdrSession), quoted(record.PaneID))
		}
	}
	if completionCount > 0 {
		fmt.Fprintf(output, "pending_completions: %d of %d\n", completionCount, len(completions))
		fmt.Fprintf(output, "pending_completions[%d]{task,mode}:\n", completionCount)
		for _, completion := range completions[:completionCount] {
			fmt.Fprintf(output, "  %s,%s\n", quoted(completion.TaskID),
				quoted(string(completion.Mode)))
		}
	}
	var hints []string
	if wakeCount > 0 {
		hints = append(hints, "tincan-inbox --root "+shellQuote(messageRoot)+
			" read "+recipient.String()+" <id>")
	}
	if wakeCount < len(wakes) {
		hints = append(hints, "tincan-inbox --root "+shellQuote(messageRoot)+
			" list "+recipient.String())
	}
	if len(backlog.rows) < backlog.total {
		hints = append(hints, "concierge-backlog --root "+shellQuote(root)+" list")
	}
	if taskCount > 0 {
		hints = append(hints, "concierge-task --root "+shellQuote(root)+" list")
	}
	if workerCount > 0 || pendingCount > 0 {
		hints = append(hints, "concierge-worker --root "+shellQuote(root)+" show <task-id>")
	}
	if workerCount < len(workers) || pendingCount < len(pending) {
		hints = append(hints, "concierge-worker --root "+shellQuote(root)+" list")
	}
	if completionCount > 0 {
		if completionCount < len(completions) {
			hints = append(hints, "concierge-task --root "+shellQuote(root)+" pending")
		}
		hints = append(hints, "concierge-backlog --root "+shellQuote(root)+
			" finish <task-id> --mode <recorded-mode>")
	}
	if len(hints) > 0 {
		encoded := make([]string, 0, len(hints))
		for _, hint := range hints {
			encoded = append(encoded, quoted(hint))
		}
		fmt.Fprintf(output, "help[%d]: %s\n", len(hints), strings.Join(encoded, ","))
	}
	return nil
}

func run(args []string, output io.Writer) int {
	if len(args) == 1 && (args[0] == "-v" || args[0] == "-V" || args[0] == "--version") {
		fmt.Fprintln(output, version)
		return 0
	}
	var options cli
	parser, err := kong.New(
		&options,
		kong.Name("concierge-brief"),
		kong.Description(
			"Show pending primary wakes, queued backlog work, local tasks, and registered workers.",
		),
		kong.Writers(output, io.Discard),
	)
	if err != nil {
		return failure(
			output,
			2,
			err,
			"concierge-brief --root ROOT [--message-root ROOT] [--recipient NAME] [--limit N]",
		)
	}
	if _, err := parser.Parse(args); err != nil {
		return failure(
			output,
			2,
			err,
			"concierge-brief --root ROOT [--message-root ROOT] [--recipient NAME] [--limit N]",
		)
	}
	if options.Limit < 1 || options.Limit > 100 {
		return failure(output, 2, fmt.Errorf("--limit must be from 1 to 100"),
			"concierge-brief --root ROOT --limit N")
	}
	if options.Root == "" || options.MessageRoot == "" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return failure(output, 1, err, "provide explicit --root and --message-root paths")
		}
		if options.Root == "" {
			options.Root = filepath.Join(workingDirectory, ".concierge")
		}
		if options.MessageRoot == "" {
			options.MessageRoot = filepath.Join(workingDirectory, ".tincan")
		}
	}
	if _, err := firstmate.ParseAbsolutePath(options.Root); err != nil {
		return failure(output, 2, err, "use an absolute, clean --root path")
	}
	if _, err := firstmate.ParseAbsolutePath(options.MessageRoot); err != nil {
		return failure(output, 2, err, "use an absolute, clean --message-root path")
	}
	recipient, err := inbox.ParseRecipient(options.Recipient)
	if err != nil {
		return failure(output, 2, err, "use a valid --recipient name")
	}
	if err := printBrief(output, options.Root, options.MessageRoot, recipient, options.Limit); err != nil {
		return failure(output, 1, err, "inspect the Concierge and Tincan state roots")
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
