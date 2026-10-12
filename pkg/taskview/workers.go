package taskview

import (
	"fmt"
	"io"

	"github.com/roman/tincan/pkg/taskstate"
)

// WriteWorkers lists confirmed bindings and separates unfinished adoptions.
func WriteWorkers(output io.Writer, tasks []taskstate.Task) error {
	registered, pending := workerTasks(tasks)
	if _, err := fmt.Fprintf(output, "count: %d\n", len(registered)); err != nil {
		return err
	}
	if err := writeWorkerRows(output, "workers", registered); err != nil {
		return err
	}
	if len(pending) > 0 {
		return writeWorkerRows(output, "pending_adoptions", pending)
	}
	return nil
}

func workerTasks(tasks []taskstate.Task) (registered, pending []taskstate.Task) {
	for _, task := range tasks {
		worker := task.Snapshot().Worker
		if worker == nil {
			continue
		}
		if worker.PendingAdoption {
			pending = append(pending, task)
			continue
		}
		registered = append(registered, task)
	}
	return registered, pending
}

func writeWorkerRows(output io.Writer, name string, tasks []taskstate.Task) error {
	if _, err := fmt.Fprintf(output, "%s[%d]{task,name,session,pane_id}:\n", name, len(tasks)); err != nil {
		return err
	}
	for _, task := range tasks {
		worker := task.Snapshot().Worker
		if _, err := fmt.Fprintf(output, "  %s,%s,%s,%s\n", quote(task.ID()), quote(worker.Name), quote(worker.Session), quote(worker.Pane)); err != nil {
			return err
		}
	}
	return nil
}

// WriteWorker shows the launch identity and all unfinished recovery markers.
func WriteWorker(output io.Writer, task taskstate.Task) error {
	worker := task.Snapshot().Worker
	if worker == nil {
		return fmt.Errorf("no worker record for task %s", task.ID())
	}
	if _, err := fmt.Fprintf(output, "task: %s\nname: %s\nkind: %s\npane_id: %s\nherdr_session: %s\nworkspace: %s\ntab: %s\nproject: %s\nworktree: %s\nspawn_generation: %s\n", task.ID(), worker.Name, worker.Harness, worker.Pane, worker.Session, worker.Workspace, worker.Tab, quote(worker.Project), quote(worker.Worktree), worker.Generation); err != nil {
		return err
	}
	if worker.PendingGeneration != "" {
		if _, err := fmt.Fprintf(output, "pending_generation: %s\n", worker.PendingGeneration); err != nil {
			return err
		}
	}
	if worker.PendingLaunch != "" {
		if _, err := fmt.Fprintf(output, "pending_launch: %s\n", worker.PendingLaunch); err != nil {
			return err
		}
	}
	if worker.PendingAdoption {
		if _, err := fmt.Fprintln(output, "adoption: pending"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(output, "agent_args[%d]:\n", len(worker.AgentArgs)); err != nil {
		return err
	}
	for _, argument := range worker.AgentArgs {
		if _, err := fmt.Fprintf(output, "  %s\n", quote(argument)); err != nil {
			return err
		}
	}
	return nil
}

// WriteLocalTasks lists tasks with confirmed worker bindings, including recovery.
func WriteLocalTasks(output io.Writer, tasks []taskstate.Task) error {
	registered, _ := workerTasks(tasks)
	if _, err := fmt.Fprintf(output, "count: %d\n", len(registered)); err != nil {
		return err
	}
	return writeLocalTaskRows(output, registered)
}

func writeLocalTaskRows(output io.Writer, tasks []taskstate.Task) error {
	if _, err := fmt.Fprintf(output, "tasks[%d]{task,kind,worktree}:\n", len(tasks)); err != nil {
		return err
	}
	for _, task := range tasks {
		if _, err := fmt.Fprintf(output, "  %s,%s,%s\n", quote(task.ID()), quote(task.Definition().Kind), quote(task.Snapshot().Worker.Worktree)); err != nil {
			return err
		}
	}
	return nil
}

// WritePendingCompletions includes accepted and retired results awaiting finish.
func WritePendingCompletions(output io.Writer, tasks []taskstate.Task) error {
	pending := completionTasks(tasks)
	if _, err := fmt.Fprintf(output, "count: %d\n", len(pending)); err != nil {
		return err
	}
	return writeCompletionRows(output, pending)
}

func completionTasks(tasks []taskstate.Task) []taskstate.Task {
	var pending []taskstate.Task
	for _, task := range tasks {
		if task.Snapshot().Completion != nil {
			pending = append(pending, task)
		}
	}
	return pending
}

func writeCompletionRows(output io.Writer, tasks []taskstate.Task) error {
	if _, err := fmt.Fprintf(output, "pending_completions[%d]{task,mode,generation,phase}:\n", len(tasks)); err != nil {
		return err
	}
	for _, task := range tasks {
		completion := task.Snapshot().Completion
		if _, err := fmt.Fprintf(output, "  %s,%s,%s,%s\n", quote(task.ID()), quote(string(completion.Mode)), quote(completion.Generation), quote(string(completion.Phase))); err != nil {
			return err
		}
	}
	return nil
}
