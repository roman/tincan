package conciergecli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskprotocol"
	"github.com/roman/tincan/pkg/taskstate"
)

func boundCLIReply(t *testing.T, root, messageRoot, generation string) inbox.Message {
	t.Helper()
	messages, _ := inbox.New(messageRoot)
	worker, _ := inbox.ParseRecipient("task-1")
	body, err := json.Marshal(
		taskprotocol.Assignment{
			Kind:            "task-assignment",
			State:           filepath.Join(root, "tasks"),
			Task:            "task-1",
			SpawnGeneration: generation,
			Body:            "Do the work",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := messages.Send(worker, "assignment", string(body))
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(
		taskprotocol.Reply{
			Kind:            "task-reply",
			State:           filepath.Join(root, "tasks"),
			Task:            "task-1",
			SpawnGeneration: generation,
			AssignmentID:    assignment.ID.String(),
			ReplyKey:        "answer",
			Body:            "Done",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var reply inbox.Message
	for _, name := range []string{"operator", "concierge"} {
		recipient, _ := inbox.ParseRecipient(name)
		reply, err = messages.Send(
			recipient,
			"task-reply:"+root+":task-1:"+generation+":"+assignment.ID.String(),
			string(body),
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	return reply
}

func TestResolveCommandUsesSelectedMailboxAndRecoversRetiredResults(t *testing.T) {
	for _, mode := range []string{"close", "retain"} {
		t.Run(mode, func(t *testing.T) {
			root := lifecycleRoot(t)
			startFixtureTask(t, root)
			messageRoot := t.TempDir()
			reply := boundCLIReply(t, root, messageRoot, "gen-1")
			args := []string{
				"--message-root",
				messageRoot,
				"--recipient",
				"operator",
				"backlog",
				"resolve",
				reply.ID.String(),
			}
			state := "done"
			if mode == "retain" {
				holdFixtureTask(t, root, "More work")
				args = append(args, "--mode", "retain")
				state = "queued"
			} else {
				args = append(args, "--closed-on", "2026-10-12")
			}
			for attempt := range 3 {
				already := "true"
				if attempt == 0 {
					already = "false"
				}
				want := "task: \"task-1\"\nstate: " + state + "\nalready: " + already + "\n"
				if got := invoke(t, root, "", args...); got != want {
					t.Fatalf("attempt %d: %s", attempt, got)
				}
				if attempt == 1 {
					store, _ := taskstate.NewStore(root)
					if _, err := store.Update(context.Background(), "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.RetireResult("gen-1") }); err != nil {
						t.Fatal(err)
					}
				}
			}
			messages, _ := inbox.New(messageRoot)
			selected, _ := inbox.ParseRecipient("operator")
			other, _ := inbox.ParseRecipient("concierge")
			if pending, err := messages.List(selected); err != nil || len(pending) != 0 {
				t.Fatalf("selected pending: %v %v", pending, err)
			}
			if pending, err := messages.List(other); err != nil || len(pending) != 1 {
				t.Fatalf("other pending: %v %v", pending, err)
			}
			if _, err := messages.ReadHandled(selected, reply.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "close" {
				conflicting := append([]string{}, args...)
				conflicting[len(conflicting)-1] = "2026-10-13"
				expectLifecycleError(t, root, 1, conflicting...)
			}
		})
	}
}

func TestResolveRejectsInvalidAndStaleInputsWithoutAcknowledgement(t *testing.T) {
	root := lifecycleRoot(t)
	startFixtureTask(t, root)
	messageRoot := t.TempDir()
	reply := boundCLIReply(t, root, messageRoot, "old-generation")
	base := []string{
		"--message-root",
		messageRoot,
		"--recipient",
		"operator",
		"backlog",
		"resolve",
		reply.ID.String(),
		"--closed-on",
		"2026-10-12",
	}
	for _, flags := range [][]string{nil, {"--recipient", "../bad"}, {"--message-root", "relative"}} {
		code := 2
		if len(flags) == 0 {
			code = 1
		}
		args := append(append([]string{}, base...), flags...)
		got := expectLifecycleError(t, root, code, args...)
		if code == 1 && !strings.Contains(got, "concierge backlog resolve REPLY_ID") {
			t.Fatalf("missing resolve recovery synopsis: %s", got)
		}
	}
	messages, _ := inbox.New(messageRoot)
	recipient, _ := inbox.ParseRecipient("operator")
	if _, err := messages.Read(recipient, reply.ID); err != nil {
		t.Fatalf("acknowledged invalid reply: %v", err)
	}
	if _, err := messages.ReadDisposition(recipient, reply.ID); !os.IsNotExist(err) {
		t.Fatalf("invalid disposition: %v", err)
	}
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil || task.State() != taskstate.InFlight {
		t.Fatalf("invalid result changed task: %#v %v", task.Snapshot(), err)
	}
	if help := invoke(t, root, "", "backlog", "resolve", "--help"); !strings.Contains(
		help,
		"--closed-on",
	) {
		t.Fatal(help)
	}
}

func TestResolveDateValidationPrecedesRootDiscovery(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("CONCIERGE_HOME", "")
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{nil, {"--closed-on", "2026-02-30"}, {"--mode", "retain", "--closed-on", "2026-10-12"}} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"backlog", "resolve", "reply-id"}, flags...)
		if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 2 ||
			!strings.Contains(stdout.String(), "--closed-on") ||
			strings.Contains(stdout.String(), "--root") ||
			stderr.Len() != 0 {
			t.Fatalf("%v: %d %s %s", args, code, &stdout, &stderr)
		}
	}
}
