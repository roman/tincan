package primary

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func readProcess(pid int) (Process, error) {
	path := fmt.Sprintf("/proc/%d", pid)
	data, err := os.ReadFile(filepath.Join(path, "stat"))
	if err != nil {
		return Process{}, err
	}
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 {
		return Process{}, errors.New("process stat lacks a command boundary")
	}
	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) <= 19 {
		return Process{}, errors.New("process stat lacks a start time")
	}
	parentPID, err := strconv.Atoi(fields[1])
	if err != nil {
		return Process{}, err
	}
	executable, err := os.Readlink(filepath.Join(path, "exe"))
	if err != nil {
		return Process{}, err
	}
	return Process{
		PID:        pid,
		ParentPID:  parentPID,
		Kind:       classifyExecutable(executable),
		StartTicks: fields[19],
	}, nil
}

func readBootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readPIDNamespace() (string, error) {
	return os.Readlink("/proc/self/ns/pid")
}
