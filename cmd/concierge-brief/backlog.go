package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type queuedBacklog struct {
	rows  []string
	total int
}

func readQueuedBacklog(root string, limit int) (queuedBacklog, error) {
	path := filepath.Join(root, "backlog.md")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return queuedBacklog{}, nil
	}
	if err != nil {
		return queuedBacklog{}, err
	}
	if !info.Mode().IsRegular() {
		return queuedBacklog{}, fmt.Errorf("backlog is not a regular file: %s", path)
	}
	output, err := exec.Command(
		"tasks-axi", "list", "--state", "queued", "--limit", strconv.Itoa(limit), "--file", path,
	).CombinedOutput()
	if err != nil {
		return queuedBacklog{}, fmt.Errorf(
			"cannot read queued backlog: %w: %s",
			err,
			strings.TrimSpace(string(output)),
		)
	}
	return parseQueuedBacklog(string(output), limit)
}

func parseQueuedBacklog(output string, limit int) (queuedBacklog, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return queuedBacklog{}, fmt.Errorf("queued backlog list has no count")
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 2 && len(fields) != 5 {
		return queuedBacklog{}, fmt.Errorf("queued backlog list has an invalid count")
	}
	if fields[0] != "count:" || (len(fields) == 5 && (fields[2] != "of" || fields[4] != "total")) {
		return queuedBacklog{}, fmt.Errorf("queued backlog list has an invalid count")
	}
	shown, err := strconv.Atoi(fields[1])
	if err != nil || shown < 0 || shown > limit {
		return queuedBacklog{}, fmt.Errorf("queued backlog list has an invalid row count")
	}
	total := shown
	if len(fields) == 5 {
		total, err = strconv.Atoi(fields[3])
		if err != nil || total <= shown {
			return queuedBacklog{}, fmt.Errorf("queued backlog list has an invalid total")
		}
	}
	if shown == 0 {
		return queuedBacklog{total: total}, nil
	}
	if len(lines) < shown+2 ||
		lines[1] != fmt.Sprintf("tasks[%d]{id,state,kind,repo,title}:", shown) {
		return queuedBacklog{}, fmt.Errorf("queued backlog list has an invalid task header")
	}
	rows := make([]string, shown)
	for index, row := range lines[2 : shown+2] {
		if !strings.HasPrefix(row, "  ") || strings.HasPrefix(row, "  - ") {
			return queuedBacklog{}, fmt.Errorf("queued backlog list has an invalid task row")
		}
		rows[index] = row
	}
	return queuedBacklog{rows: rows, total: total}, nil
}
