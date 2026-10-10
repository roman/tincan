package main

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func showBacklog(output io.Writer, path string, selection showCommand) error {
	arguments := []string{"show", selection.TaskID, "--file", path}
	if selection.Full {
		arguments = append(arguments, "--full")
	}
	result, err := exec.Command("tasks-axi", arguments...).CombinedOutput()
	if err != nil {
		if strings.Contains(string(result), "\ncode: NOT_FOUND\n") {
			return fmt.Errorf("task %q not found in this backlog", selection.TaskID)
		}
		return fmt.Errorf("cannot show backlog task %q: %w", selection.TaskID, err)
	}
	_, err = output.Write(result)
	return err
}
