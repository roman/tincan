package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman/tincan/pkg/stateversion"
)

func TestBacklogWriterKeepsVersionLeaseUntilExit(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	realTasksAXI, err := exec.LookPath("tasks-axi")
	if err != nil {
		t.Skip("tasks-axi is not installed")
	}
	directory := t.TempDir()
	stateRoot := filepath.Join(directory, "state")
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	backlogCommand := filepath.Join(directory, "concierge-backlog")
	build := exec.Command(
		"go",
		"build",
		"-buildvcs=false",
		"-o",
		backlogCommand,
		"./cmd/concierge-backlog",
	)
	build.Dir = filepath.Dir(workingDirectory)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build backlog command: %v: %s", err, output)
	}
	wrapperDirectory := filepath.Join(directory, "bin")
	if err := os.Mkdir(wrapperDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(directory, "ready")
	release := filepath.Join(directory, "release")
	wrapper := "#!/bin/sh\nif [ \"$1\" = add ]; then\n" +
		"  touch \"$LEASE_READY\"\n" +
		"  while [ ! -e \"$LEASE_RELEASE\" ]; do sleep 0.01; done\n" +
		"fi\nexec \"$REAL_TASKS_AXI\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapperDirectory, "tasks-axi"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(backlogCommand, "--root", stateRoot, "add", "task-lease", "Lease test",
		"--kind", "task", "--repo", "exmachina")
	command.Env = append(os.Environ(), "PATH="+wrapperDirectory+":"+os.Getenv("PATH"),
		"LEASE_READY="+ready, "LEASE_RELEASE="+release, "REAL_TASKS_AXI="+realTasksAXI)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, nil, 0o600)
		if command.ProcessState == nil {
			_ = command.Wait()
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backlog writer did not reach tasks-axi")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := stateversion.AcquireMigration(ctx, stateRoot); !errors.Is(
		err,
		context.DeadlineExceeded,
	) {
		t.Fatalf("migration acquired lock while writer ran: %v", err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("backlog writer failed: %v", err)
	}
	migration, err := stateversion.AcquireMigration(context.Background(), stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyTaskCommandsRefuseVersionTwo(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	commands := filepath.Join(root, "commands")
	if err := os.Mkdir(commands, 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", commands,
		"./cmd/concierge-backlog", "./cmd/concierge-brief", "./cmd/concierge-supervise",
		"./cmd/concierge-task", "./cmd/concierge-worker", "./cmd/tincan-task-send",
		"./cmd/tincan-task-receive", "./cmd/tincan-task-reply")
	build.Dir = filepath.Dir(workingDirectory)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build guarded commands: %v: %s", err, output)
	}
	stateRoot := filepath.Join(root, "state")
	messageRoot := filepath.Join(root, "messages")
	for _, directory := range []string{stateRoot, messageRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stateRoot, ".task-state-version"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bodyFile := filepath.Join(root, "body.txt")
	if err := os.WriteFile(bodyFile, []byte("Task body"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		command string
		args    []string
	}{
		{"concierge-backlog", []string{"--root", stateRoot, "list"}},
		{"concierge-brief", []string{"--root", stateRoot, "--message-root", messageRoot}},
		{
			"concierge-supervise",
			[]string{"--root", stateRoot, "--message-root", messageRoot, "--task", "task-1"},
		},
		{"concierge-task", []string{"--root", stateRoot, "list"}},
		{"concierge-worker", []string{"--root", stateRoot, "list"}},
		{
			"tincan-task-send",
			[]string{
				"--task-root",
				stateRoot,
				"--message-root",
				messageRoot,
				"task-1",
				"--body-file",
				bodyFile,
			},
		},
		{
			"tincan-task-receive",
			[]string{
				"--task-root",
				stateRoot,
				"--message-root",
				messageRoot,
				"read",
				"task-1",
				"k-1",
				"--generation",
				"gen-1",
			},
		},
		{
			"tincan-task-reply",
			[]string{"--task-root", stateRoot, "--message-root", messageRoot, "check", "k-1"},
		},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			command := exec.Command(filepath.Join(commands, test.command), test.args...)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "task-state version") {
				t.Fatalf("legacy command reached version 2 state: %v: %s", err, output)
			}
		})
	}
}
