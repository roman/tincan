package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
)

func boundReply(t *testing.T, root, messages string) inbox.Message {
	t.Helper()
	store, err := inbox.New(messages)
	if err != nil {
		t.Fatal(err)
	}
	worker, _ := inbox.ParseRecipient("task-1")
	primary, _ := inbox.ParseRecipient("firstmate")
	assignmentBody, err := json.Marshal(taskprotocol.Assignment{
		Kind: "task-assignment", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: "gen-1", Body: "Do the task",
	})
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := store.Send(worker, "assignment-1", string(assignmentBody))
	if err != nil {
		t.Fatal(err)
	}
	replyBody, err := json.Marshal(taskprotocol.Reply{
		Kind: "task-reply", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: "gen-1", AssignmentID: assignment.ID.String(),
		ReplyKey: "answer-1", Body: "Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.Send(primary,
		"task-reply:"+root+":task-1:gen-1:"+assignment.ID.String(), string(replyBody))
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func resolveCLI(root, messages string, reply inbox.Message) (int, string) {
	var output bytes.Buffer
	code := run([]string{"--root", root, "resolve", reply.ID.String(),
		"--message-root", messages, "--mode", "close"}, &output)
	return code, output.String()
}

func TestResolveReplyFinishesAndAcknowledges(t *testing.T) {
	root := backlogRoot(t)
	messages := filepath.Join(t.TempDir(), "messages")
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	reply := boundReply(t, root, messages)
	for attempt := range 2 {
		code, output := resolveCLI(root, messages, reply)
		if code != 0 || !strings.Contains(output, "state: done") {
			t.Fatalf("attempt %d: code=%d output=%s", attempt, code, output)
		}
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: done") {
		t.Fatal("reply did not finish task")
	}
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	if _, err := store.ReadHandled(primary, reply.ID); err != nil {
		t.Fatalf("reply was not acknowledged: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "tasks", "task-1.completion.json")); err != nil {
		t.Fatalf("completion marker lost: %v", err)
	}
}

func TestResolveReplyResumesAfterAcceptedResult(t *testing.T) {
	root := backlogRoot(t)
	messages := filepath.Join(t.TempDir(), "messages")
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	reply := boundReply(t, root, messages)
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	action := "finish:task-1:gen-1"
	if _, _, err := store.BeginDisposition(primary, reply.ID, action); err != nil {
		t.Fatal(err)
	}
	if code, output := finishCLI(t, root, "close"); code != 0 {
		t.Fatalf("finish before retry: %s", output)
	}
	code, output := resolveCLI(root, messages, reply)
	if code != 0 || !strings.Contains(output, "state: done") {
		t.Fatalf("retry: code=%d output=%s", code, output)
	}
	disposition, err := store.ReadDisposition(primary, reply.ID)
	if err != nil || disposition.State != inbox.DispositionComplete {
		t.Fatalf("disposition=%+v err=%v", disposition, err)
	}
}

func TestResolveReplyRejectsChangedWorkerGeneration(t *testing.T) {
	root := backlogRoot(t)
	messages := filepath.Join(t.TempDir(), "messages")
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	reply := boundReply(t, root, messages)
	changeWorkerGeneration(t, root, "gen-2")
	if code, output := resolveCLI(root, messages, reply); code == 0 {
		t.Fatalf("stale reply resolved: %s", output)
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: in_flight") {
		t.Fatal("stale reply changed backlog")
	}
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	if _, err := store.Read(primary, reply.ID); err != nil {
		t.Fatalf("stale reply was acknowledged: %v", err)
	}
}

func changeWorkerGeneration(t *testing.T, root, generation string) {
	t.Helper()
	metaPath := filepath.Join(root, "tasks", "task-1.meta")
	metadata, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath,
		[]byte(strings.Replace(string(metadata), "spawn_gen=gen-1", "spawn_gen="+generation, 1)),
		0o600); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "crew", "task-1.json")
	recordData, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record crew.Record
	if err := json.Unmarshal(recordData, &record); err != nil {
		t.Fatal(err)
	}
	record.SpawnGeneration = generation
	recordData, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, recordData, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveReplyRejectsUnboundReply(t *testing.T) {
	root := backlogRoot(t)
	messages := filepath.Join(t.TempDir(), "messages")
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	body, err := json.Marshal(taskprotocol.Reply{
		Kind: "task-reply", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: "gen-1", Body: "Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := store.Send(primary, "legacy", string(body))
	if err != nil {
		t.Fatal(err)
	}
	if code, output := resolveCLI(root, messages, reply); code == 0 {
		t.Fatalf("unbound reply resolved: %s", output)
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: in_flight") {
		t.Fatal("unbound reply changed backlog")
	}
}

func TestResolveReplyResumesAfterCleanup(t *testing.T) {
	root := backlogRoot(t)
	messages := filepath.Join(t.TempDir(), "messages")
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	reply := boundReply(t, root, messages)
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	if _, _, err := store.BeginDisposition(primary, reply.ID, "finish:task-1:gen-1"); err != nil {
		t.Fatal(err)
	}
	if code, output := finishCLI(t, root, "close"); code != 0 {
		t.Fatalf("finish before cleanup: %s", output)
	}
	var cleanupOutput bytes.Buffer
	if code := runWithClient(context.Background(),
		[]string{"--root", root, "cleanup", "task-1"}, &cleanupOutput,
		herdr.Client{SessionRunner: finishRunner{response: `{"error":{"code":"pane_not_found"}}`}},
	); code != 0 {
		t.Fatalf("cleanup before retry: %s", cleanupOutput.String())
	}
	if code, output := resolveCLI(root, messages, reply); code != 0 ||
		!strings.Contains(output, "already: true") {
		t.Fatalf("retry after cleanup: code=%d output=%s", code, output)
	}
}

func TestResolveReplyRejectsOlderGenerationAfterNewCleanup(t *testing.T) {
	root := backlogRoot(t)
	messages := filepath.Join(t.TempDir(), "messages")
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	reply := boundReply(t, root, messages)
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	if _, _, err := store.BeginDisposition(primary, reply.ID, "finish:task-1:gen-1"); err != nil {
		t.Fatal(err)
	}
	changeWorkerGeneration(t, root, "gen-2")
	if code, output := finishCLI(t, root, "close"); code != 0 {
		t.Fatalf("new generation finish: %s", output)
	}
	var cleanupOutput bytes.Buffer
	if code := runWithClient(context.Background(),
		[]string{"--root", root, "cleanup", "task-1"}, &cleanupOutput,
		herdr.Client{SessionRunner: finishRunner{response: `{"error":{"code":"pane_not_found"}}`}},
	); code != 0 {
		t.Fatalf("new generation cleanup: %s", cleanupOutput.String())
	}
	if code, output := resolveCLI(root, messages, reply); code == 0 {
		t.Fatalf("older reply resolved after newer cleanup: %s", output)
	}
	if _, err := store.Read(primary, reply.ID); err != nil {
		t.Fatalf("older reply was acknowledged: %v", err)
	}
}

func TestResolveReplyRejectsHandledWithoutDisposition(t *testing.T) {
	root := backlogRoot(t)
	messages := filepath.Join(t.TempDir(), "messages")
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	reply := boundReply(t, root, messages)
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	if err := store.Acknowledge(primary, reply.ID); err != nil {
		t.Fatal(err)
	}
	if code, output := resolveCLI(root, messages, reply); code == 0 {
		t.Fatalf("handled reply resolved: %s", output)
	}
	if !strings.Contains(backlogState(t, root, "task-1"), "state: in_flight") {
		t.Fatal("handled reply changed backlog")
	}
}

func TestResolveReplyCanRetainAfterRejectedClose(t *testing.T) {
	root := backlogRoot(t)
	messages := filepath.Join(t.TempDir(), "messages")
	queueTask(t, root, "task-1")
	bindWorker(t, root, "task-1")
	startBoundTask(t, root)
	reply := boundReply(t, root, messages)
	output, err := exec.Command("tasks-axi", "hold", "task-1", "--reason", "Waiting",
		"--file", filepath.Join(root, "backlog.md")).CombinedOutput()
	if err != nil {
		t.Fatalf("hold: %v: %s", err, output)
	}
	if code, result := resolveCLI(root, messages, reply); code == 0 {
		t.Fatalf("held close resolved: %s", result)
	}
	var retained bytes.Buffer
	code := run([]string{"--root", root, "resolve", reply.ID.String(),
		"--message-root", messages, "--mode", "retain"}, &retained)
	if code != 0 || !strings.Contains(retained.String(), "state: queued") {
		t.Fatalf("retain: code=%d output=%s", code, retained.String())
	}
}

func TestResolveReplyRequiresAbsoluteMessageRoot(t *testing.T) {
	root := backlogRoot(t)
	var output bytes.Buffer
	code := run([]string{"--root", root, "resolve", "k-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"--message-root", "relative", "--mode", "close"}, &output)
	if code != 2 || !strings.Contains(output.String(), "absolute") {
		t.Fatalf("relative message root: code=%d output=%s", code, output.String())
	}
}
