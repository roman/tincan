package main

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func listBacklog(output io.Writer, path, state string) error {
	result, err := exec.Command("tasks-axi", "list", "--state", state, "--file", path).
		CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"cannot list backlog tasks: %w: %s",
			err,
			strings.TrimSpace(string(result)),
		)
	}
	view := string(result)
	if helpIndex := strings.Index(view, "\nhelp["); helpIndex >= 0 {
		view = view[:helpIndex]
	}
	_, err = fmt.Fprintln(output, strings.TrimRight(view, "\n"))
	return err
}
