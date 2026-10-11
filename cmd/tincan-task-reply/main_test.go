package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/inbox"
)

func replyFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	tasks := filepath.Join(root, "tasks")
	if err := os.Mkdir(tasks, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := strings.Join([]string{
		"window=work:pane-1", "endpoint_task_id=task-1", "worktree=" + worktree,
		"project=" + root, "harness=claude", "kind=worker", "spawn_gen=gen-1",
		"backend=herdr", "herdr_session=work", "herdr_workspace_id=workspace-1",
		"herdr_tab_id=tab-1", "herdr_pane_id=pane-1", "",
	}, "\n")
	if err := os.WriteFile(filepath.Join(tasks, "task-1.meta"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := crew.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(crew.Record{
		TaskID: "task-1", Name: "worker-1", Kind: "claude", PaneID: "pane-1",
		HerdrSession: "work", Project: root, Worktree: worktree,
		SpawnGeneration: "gen-1", AgentArgs: []string{"--resume", "session-1"},
	}); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "messages")
}

func TestReplySendAndCheckCurrentGeneration(t *testing.T) {
	root, messages := replyFixture(t)
	args := []string{"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"}
	var output, errors bytes.Buffer
	if code := run(args, strings.NewReader("Done.\n"), &output, &errors); code != 0 {
		t.Fatalf("send code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	var envelope map[string]string
	if err := json.Unmarshal([]byte(pending[0].Body), &envelope); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"kind": "task-reply", "state": filepath.Join(root, "tasks"),
		"task": "task-1", "spawn_generation": "gen-1", "body": "Done.\n",
	} {
		if envelope[key] != want {
			t.Fatalf("envelope[%s]=%q, want %q", key, envelope[key], want)
		}
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", pending[0].ID.String()}, strings.NewReader(""), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "status: current\n") ||
		!strings.Contains(output.String(), `body: "Done.\n"`) {
		t.Fatalf("check code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
	output.Reset()
	errors.Reset()
	if code := run(args, strings.NewReader("Done.\n"), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "id: "+pending[0].ID.String()) {
		t.Fatalf("retry code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
}

func TestReplyAcknowledgesAssignmentAfterDurableSend(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	worker, _ := inbox.ParseRecipient("task-1")
	assignmentBody, _ := json.Marshal(map[string]string{
		"kind": "task-assignment", "state": filepath.Join(root, "tasks"),
		"task": "task-1", "spawn_generation": "gen-1", "body": "Build feature",
	})
	assignment, err := store.Send(worker, "assignment-1", string(assignmentBody))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1",
		"--assignment-id", assignment.ID.String(), "--body-file", "-",
	}
	for range 2 {
		var output, errors bytes.Buffer
		if code := run(args, strings.NewReader("Done"), &output, &errors); code != 0 ||
			!strings.Contains(output.String(), "assignment: acknowledged\n") {
			t.Fatalf("code=%d output=%q errors=%q", code, output.String(), errors.String())
		}
	}
	pending, err := store.List(worker)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending assignments=%+v err=%v", pending, err)
	}
	primary, _ := inbox.ParseRecipient("firstmate")
	replies, err := store.List(primary)
	if err != nil || len(replies) != 1 {
		t.Fatalf("pending replies=%+v err=%v", replies, err)
	}
	var checked, checkErrors bytes.Buffer
	if code := run([]string{
		"--task-root", root, "--message-root", messages,
		"check", replies[0].ID.String(),
	}, strings.NewReader(""), &checked, &checkErrors); code != 0 ||
		!strings.Contains(checked.String(), "status: current\n") {
		t.Fatalf("check code=%d output=%q errors=%q", code, checked.String(), checkErrors.String())
	}
}

func TestReplyRetryAfterPublicationAcknowledgesAssignment(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	worker, _ := inbox.ParseRecipient("task-1")
	assignmentBody, _ := json.Marshal(map[string]string{
		"kind": "task-assignment", "state": filepath.Join(root, "tasks"),
		"task": "task-1", "spawn_generation": "gen-1", "body": "Build feature",
	})
	assignment, err := store.Send(worker, "assignment-1", string(assignmentBody))
	if err != nil {
		t.Fatal(err)
	}
	primary, _ := inbox.ParseRecipient("firstmate")
	replyBody, _ := json.Marshal(replyEnvelope{
		Kind: "task-reply", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: "gen-1",
		AssignmentID: assignment.ID.String(), ReplyKey: "answer-1", Body: "Done",
	})
	published, err := store.Send(primary,
		"task-reply:"+root+":task-1:gen-1:"+assignment.ID.String(), string(replyBody))
	if err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	code := run([]string{
		"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1",
		"--assignment-id", assignment.ID.String(), "--body-file", "-",
	}, strings.NewReader("Done"), &output, &errors)
	if code != 0 || !strings.Contains(output.String(), "id: "+published.ID.String()+"\n") {
		t.Fatalf("code=%d output=%q errors=%q", code, output.String(), errors.String())
	}
	assignments, err := store.List(worker)
	if err != nil || len(assignments) != 0 {
		t.Fatalf("pending assignments=%+v err=%v", assignments, err)
	}
	replies, err := store.List(primary)
	if err != nil || len(replies) != 1 {
		t.Fatalf("pending replies=%+v err=%v", replies, err)
	}
}

func TestReplyRejectsAssignmentFromAnotherGeneration(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	worker, _ := inbox.ParseRecipient("task-1")
	assignmentBody, _ := json.Marshal(map[string]string{
		"kind": "task-assignment", "state": filepath.Join(root, "tasks"),
		"task": "task-1", "spawn_generation": "gen-old", "body": "Old work",
	})
	assignment, err := store.Send(worker, "old-assignment", string(assignmentBody))
	if err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	code := run([]string{
		"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1",
		"--assignment-id", assignment.ID.String(), "--body-file", "-",
	}, strings.NewReader("Done"), &output, &errors)
	if code == 0 {
		t.Fatalf("old assignment accepted: %q", output.String())
	}
	primary, _ := inbox.ParseRecipient("firstmate")
	replies, err := store.List(primary)
	if err != nil || len(replies) != 0 {
		t.Fatalf("replies=%+v err=%v", replies, err)
	}
	if _, err := store.Read(worker, assignment.ID); err != nil {
		t.Fatalf("old assignment was acknowledged: %v", err)
	}
}

func TestAssignmentIdentifiesOneReplyAcrossKeys(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	worker, _ := inbox.ParseRecipient("task-1")
	primary, _ := inbox.ParseRecipient("firstmate")
	for index, key := range []string{"first", "second"} {
		assignmentBody, _ := json.Marshal(map[string]string{
			"kind": "task-assignment", "state": filepath.Join(root, "tasks"),
			"task": "task-1", "spawn_generation": "gen-1", "body": key,
		})
		assignment, err := store.Send(worker, key, string(assignmentBody))
		if err != nil {
			t.Fatal(err)
		}
		for _, replyKey := range []string{"answer-1", "answer-2"} {
			var output, errors bytes.Buffer
			code := run([]string{
				"--task-root", root, "--message-root", messages,
				"send", "task-1", "--generation", "gen-1", "--key", replyKey,
				"--assignment-id", assignment.ID.String(), "--body-file", "-",
			}, strings.NewReader("Done"), &output, &errors)
			if replyKey == "answer-1" && code != 0 {
				t.Fatalf("code=%d output=%q errors=%q", code, output.String(), errors.String())
			}
			if replyKey == "answer-2" && code == 0 {
				t.Fatalf("changed reply key succeeded: %q", output.String())
			}
		}
		replies, err := store.List(primary)
		if err != nil || len(replies) != index+1 {
			t.Fatalf("replies=%+v err=%v", replies, err)
		}
		var reply replyEnvelope
		if err := json.Unmarshal([]byte(replies[index].Body), &reply); err != nil ||
			reply.AssignmentID != assignment.ID.String() {
			t.Fatalf("reply=%+v err=%v", reply, err)
		}
	}
}

func TestReplyRejectsOldGenerationAtSendAndCheck(t *testing.T) {
	root, messages := replyFixture(t)
	var output, errors bytes.Buffer
	args := []string{"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"}
	if code := run(args, strings.NewReader("Finished"), &output, &errors); code != 0 {
		t.Fatalf("send code=%d errors=%s", code, errors.String())
	}
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	metaPath := filepath.Join(root, "tasks", "task-1.meta")
	metadata, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath,
		[]byte(strings.Replace(string(metadata), "spawn_gen=gen-1", "spawn_gen=gen-2", 1)),
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
	record.SpawnGeneration = "gen-2"
	recordData, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, recordData, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", pending[0].ID.String()}, strings.NewReader(""), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "status: stale\n") ||
		strings.Contains(output.String(), "body:") {
		t.Fatalf("stale check code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
	output.Reset()
	errors.Reset()
	if code := run(args, strings.NewReader("Finished"), &output, &errors); code == 0 {
		t.Fatalf("stale send succeeded: %s", output.String())
	}
	pending, err = store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("stale send changed inbox: %+v err=%v", pending, err)
	}
}

func TestReplyCheckRejectsOrdinaryMessage(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	message, err := store.Send(recipient, "ordinary", "Unbound text")
	if err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", message.ID.String()}, strings.NewReader(""), &output, &errors); code == 0 ||
		strings.Contains(output.String(), "status: current") {
		t.Fatalf("ordinary message accepted: code=%d output=%s", code, output.String())
	}
}

func TestReplyCheckRejectsMissingBoundAssignment(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	missing, _ := inbox.ParseMessageID("k-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	body, _ := json.Marshal(replyEnvelope{
		Kind: "task-reply", State: filepath.Join(root, "tasks"), Task: "task-1",
		SpawnGeneration: "gen-1", AssignmentID: missing.String(), ReplyKey: "answer-1",
		Body: "Done",
	})
	message, err := store.Send(primary,
		"task-reply:"+root+":task-1:gen-1:"+missing.String(), string(body))
	if err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	code := run([]string{"--task-root", root, "--message-root", messages,
		"check", message.ID.String()}, strings.NewReader(""), &output, &errors)
	if code != 0 || !strings.Contains(output.String(), "status: stale\n") ||
		strings.Contains(output.String(), "body:") {
		t.Fatalf("missing assignment accepted: code=%d output=%q", code, output.String())
	}
}

func TestReplyCheckRejectsMalformedBoundReply(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	assignmentID := "k-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name     string
		replyKey string
		key      string
	}{
		{name: "blank reply key", key: "task-reply:" + root + ":task-1:gen-1:" + assignmentID},
		{name: "wrong inbox key", replyKey: "answer-1", key: "unrelated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(replyEnvelope{
				Kind: "task-reply", State: filepath.Join(root, "tasks"), Task: "task-1",
				SpawnGeneration: "gen-1", AssignmentID: assignmentID,
				ReplyKey: test.replyKey, Body: "Done",
			})
			message, err := store.Send(primary, test.key, string(body))
			if err != nil {
				t.Fatal(err)
			}
			var output, errors bytes.Buffer
			code := run([]string{"--task-root", root, "--message-root", messages,
				"check", message.ID.String()}, strings.NewReader(""), &output, &errors)
			if code == 0 {
				t.Fatalf("malformed reply accepted: %q", output.String())
			}
		})
	}
}

func TestLegacyReplyRetryKeepsItsEnvelope(t *testing.T) {
	root, messages := replyFixture(t)
	store, _ := inbox.New(messages)
	primary, _ := inbox.ParseRecipient("firstmate")
	body, _ := json.Marshal(replyEnvelope{
		Kind: "task-reply", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: "gen-1", Body: "Done",
	})
	previous, err := store.Send(primary, "task-reply:"+root+":task-1:gen-1:answer-1", string(body))
	if err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	code := run([]string{
		"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-",
	}, strings.NewReader("Done"), &output, &errors)
	if code != 0 || !strings.Contains(output.String(), "id: "+previous.ID.String()+"\n") {
		t.Fatalf("legacy retry code=%d output=%q errors=%q", code, output.String(), errors.String())
	}
}

func TestReplyUsesSameTaskStateThroughSymlink(t *testing.T) {
	root, messages := replyFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	var output, errors bytes.Buffer
	if code := run([]string{"--task-root", alias, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"},
		strings.NewReader("Done"), &output, &errors); code != 0 {
		t.Fatalf("send code=%d errors=%s", code, errors.String())
	}
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", pending[0].ID.String()}, strings.NewReader(""), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "status: current\n") {
		t.Fatalf("check code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"},
		strings.NewReader("Done"), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "id: "+pending[0].ID.String()) {
		t.Fatalf("retry code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
}

func TestReplyCheckTreatsRemovedTaskAsStale(t *testing.T) {
	root, messages := replyFixture(t)
	var output, errors bytes.Buffer
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", "gen-1", "--key", "answer-1", "--body-file", "-"},
		strings.NewReader("Done"), &output, &errors); code != 0 {
		t.Fatalf("send code=%d errors=%s", code, errors.String())
	}
	store, _ := inbox.New(messages)
	recipient, _ := inbox.ParseRecipient("firstmate")
	pending, err := store.List(recipient)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := os.Remove(filepath.Join(root, "tasks", "task-1.meta")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	errors.Reset()
	if code := run([]string{"--task-root", root, "--message-root", messages,
		"check", pending[0].ID.String()}, strings.NewReader(""), &output, &errors); code != 0 ||
		!strings.Contains(output.String(), "status: stale\n") ||
		strings.Contains(output.String(), "body:") {
		t.Fatalf("check code=%d output=%s errors=%s", code, output.String(), errors.String())
	}
}
