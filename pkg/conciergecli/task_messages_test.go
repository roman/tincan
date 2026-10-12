package conciergecli

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
)

func assignmentMessage(t *testing.T, root, messageRoot, generation, body string) inbox.Message {
	t.Helper()
	messages, err := inbox.New(messageRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	envelope, err := json.Marshal(
		taskprotocol.Assignment{
			Kind:            "task-assignment",
			State:           filepath.Join(root, "tasks"),
			Task:            "task-1",
			SpawnGeneration: generation,
			Body:            body,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	message, err := messages.Send(recipient, generation, string(envelope))
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func TestAssignmentReceiveAndAckRespectCallerGeneration(t *testing.T) {
	root := lifecycleRoot(t)
	startFixtureTask(t, root)
	messageRoot := t.TempDir()
	body := "Full assignment\nQuote: \"value\"\n" + strings.Repeat("✓", 600)
	encodedBody, _ := json.Marshal(body)
	current := assignmentMessage(t, root, messageRoot, "gen-1", body)
	stale := assignmentMessage(t, root, messageRoot, "old-generation", "Hidden stale work")
	globals := []string{"--message-root", messageRoot, "--recipient", "../unused"}
	receive := func(id, generation string) string {
		t.Setenv(workerLaunchGeneration, generation)
		return invoke(
			t,
			root,
			"",
			append(
				append([]string{}, globals...),
				"task",
				"receive",
				"task-1",
				id,
				"--generation",
				generation,
			)...)
	}
	ack := func(id, generation string) string {
		t.Setenv(workerLaunchGeneration, generation)
		return invoke(
			t,
			root,
			"",
			append(
				append([]string{}, globals...),
				"task",
				"ack",
				"task-1",
				id,
				"--generation",
				generation,
			)...)
	}
	if got := receive(current.ID.String(), "gen-1"); got != "status: current\nbody: "+string(
		encodedBody,
	)+"\n" {
		t.Fatalf("current: %s", got)
	}
	for _, pair := range [][2]string{{current.ID.String(), "old-generation"}, {stale.ID.String(), "gen-1"}} {
		if got := receive(pair[0], pair[1]); got != "status: stale\n" {
			t.Fatalf("stale: %s", got)
		}
	}
	if got := ack(current.ID.String(), "old-generation"); got != "status: stale\n" {
		t.Fatal(got)
	}
	messages, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("task-1")
	if _, err := messages.Read(recipient, current.ID); err != nil {
		t.Fatalf("old caller acknowledged current assignment: %v", err)
	}
	if got := ack(stale.ID.String(), "gen-1"); got != "status: stale\n" {
		t.Fatal(got)
	}
	if _, err := messages.ReadHandled(recipient, stale.ID); err != nil {
		t.Fatalf("current caller failed stale disposal: %v", err)
	}
	for _, id := range []string{current.ID.String(), stale.ID.String()} {
		for range 2 {
			if got := ack(id, "gen-1"); got != "status: acknowledged\n" {
				t.Fatal(got)
			}
		}
	}
}

func TestAssignmentUsageAndOutputFailures(t *testing.T) {
	t.Setenv(workerLaunchGeneration, "gen-1")
	root := lifecycleRoot(t)
	startFixtureTask(t, root)
	messageRoot := t.TempDir()
	message := assignmentMessage(t, root, messageRoot, "gen-1", "Work")
	for _, operation := range []string{"receive", "ack"} {
		args := []string{
			"--message-root",
			messageRoot,
			"task",
			operation,
			"task-1",
			message.ID.String(),
		}
		expectLifecycleError(t, root, 2, args...)
		expectLifecycleError(t, root, 2, append(args, "--generation", "gen-1", "--typo")...)
		if code := Run(context.Background(), append(append([]string{"--root", root}, args...), "--generation", "gen-1"), strings.NewReader(""), failedOutput{}, io.Discard); code != 1 {
			t.Fatalf("%s writer exit%d", operation, code)
		}
	}
}
