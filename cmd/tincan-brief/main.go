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
	Root      string `help:"Absolute Tincan state root; defaults to .tincan in the current directory." env:"TINCAN_HOME"`
	Recipient string `help:"Primary inbox recipient." default:"firstmate"`
	Limit     int    `help:"Maximum rows from each source." default:"10"`
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

func printBrief(output io.Writer, root string, recipient inbox.Recipient, limit int) error {
	registry, err := crew.New(root)
	if err != nil {
		return err
	}
	store, err := inbox.New(root)
	if err != nil {
		return err
	}
	wakes, err := store.List(recipient)
	if err != nil {
		return err
	}
	workers, err := registry.List()
	if err != nil {
		return err
	}
	wakeCount := min(len(wakes), limit)
	workerCount := min(len(workers), limit)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "bin: %s\n", quoted(executable))
	fmt.Fprintf(
		output,
		"description: %s\n",
		quoted("Pending primary wakes and registered local workers"),
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
	if workerCount == len(workers) {
		fmt.Fprintf(output, "crew: %d registered\n", workerCount)
	} else {
		fmt.Fprintf(output, "crew: %d of %d registered\n", workerCount, len(workers))
	}
	if workerCount > 0 {
		fmt.Fprintf(output, "crew[%d]{task,name,session,pane_id}:\n", workerCount)
		for _, worker := range workers[:workerCount] {
			fmt.Fprintf(output, "  %s,%s,%s,%s\n", quoted(worker.TaskID), quoted(worker.Name),
				quoted(worker.HerdrSession), quoted(worker.PaneID))
		}
	}
	var hints []string
	if wakeCount > 0 {
		hints = append(hints, "tincan-inbox --root "+shellQuote(root)+
			" read "+recipient.String()+" <id>")
	}
	if wakeCount < len(wakes) {
		hints = append(hints, "tincan-inbox --root "+shellQuote(root)+
			" list "+recipient.String())
	}
	if workerCount > 0 {
		hints = append(hints, "tincan-crew --root "+shellQuote(root)+" show <task-id>")
	}
	if workerCount < len(workers) {
		hints = append(hints, "tincan-crew --root "+shellQuote(root)+" list")
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
	parser, err := kong.New(&options, kong.Name("tincan-brief"),
		kong.Description("Show pending primary wakes and registered local workers."),
		kong.Writers(output, io.Discard))
	if err != nil {
		return failure(output, 2, err, "tincan-brief --root ROOT [--recipient NAME] [--limit N]")
	}
	if _, err := parser.Parse(args); err != nil {
		return failure(output, 2, err, "tincan-brief --root ROOT [--recipient NAME] [--limit N]")
	}
	if options.Limit < 1 || options.Limit > 100 {
		return failure(output, 2, fmt.Errorf("--limit must be from 1 to 100"),
			"tincan-brief --root ROOT --limit N")
	}
	if options.Root == "" {
		workingDirectory, err := os.Getwd()
		if err != nil {
			return failure(output, 1, err, "provide an explicit --root path")
		}
		options.Root = filepath.Join(workingDirectory, ".tincan")
	}
	if _, err := firstmate.ParseAbsolutePath(options.Root); err != nil {
		return failure(output, 2, err, "use an absolute, clean --root path")
	}
	recipient, err := inbox.ParseRecipient(options.Recipient)
	if err != nil {
		return failure(output, 2, err, "use a valid --recipient name")
	}
	if err := printBrief(output, options.Root, recipient, options.Limit); err != nil {
		return failure(output, 1, err, "inspect the Tincan state root")
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
