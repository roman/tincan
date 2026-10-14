package taskprotocol

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
)

func TestAssignmentRejectsBrokenLineage(t *testing.T) {
	for _, change := range []string{"same generation", "changed body", "wrong original", "wrong task", "wrong root", "missing predecessor"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			store, err := inbox.New(filepath.Join(t.TempDir(), "messages"))
			if err != nil {
				t.Fatal(err)
			}
			recipient, _ := inbox.ParseRecipient("task-1")
			source := Assignment{
				Kind:            "task-assignment",
				State:           filepath.Join(root, "tasks"),
				Task:            "task-1",
				SpawnGeneration: "gen-1",
				Body:            "Do work",
			}
			if change == "wrong task" {
				source.Task = "task-2"
			}
			if change == "wrong root" {
				source.State = filepath.Join(t.TempDir(), "tasks")
			}
			body, _ := json.Marshal(source)
			original, err := store.Send(recipient, "original", string(body))
			if err != nil {
				t.Fatal(err)
			}
			assignment := Assignment{
				Kind:              "task-assignment",
				State:             filepath.Join(root, "tasks"),
				Task:              "task-1",
				SpawnGeneration:   "gen-2",
				Body:              "Do work",
				OriginalMessageID: original.ID.String(),
				PreviousMessageID: original.ID.String(),
			}
			switch change {
			case "same generation":
				assignment.SpawnGeneration = "gen-1"
			case "changed body":
				assignment.Body = "Changed work"
			case "wrong original", "missing predecessor":
				unrelated, err := store.Send(recipient, "unrelated", "Unrelated")
				if err != nil {
					t.Fatal(err)
				}
				if change == "wrong original" {
					assignment.OriginalMessageID = unrelated.ID.String()
				} else {
					assignment.PreviousMessageID = unrelated.ID.String()
				}
			}
			body, _ = json.Marshal(assignment)
			delivery, err := store.Send(
				recipient,
				RedeliveryKey(
					root,
					"task-1",
					assignment.SpawnGeneration,
					assignment.OriginalMessageID,
				),
				string(body),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReadAssignment(store, root, "task-1", delivery.ID); err == nil {
				t.Fatalf("accepted %s", change)
			}
		})
	}
}

func TestAssignmentRejectsGenerationCycle(t *testing.T) {
	root := t.TempDir()
	store, err := inbox.New(filepath.Join(t.TempDir(), "messages"))
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	source := Assignment{
		Kind:            "task-assignment",
		State:           filepath.Join(root, "tasks"),
		Task:            "task-1",
		SpawnGeneration: "gen-1",
		Body:            "Do work",
	}
	body, _ := json.Marshal(source)
	original, err := store.Send(recipient, "original", string(body))
	if err != nil {
		t.Fatal(err)
	}
	predecessor := original.ID.String()
	for _, generation := range []string{"gen-2", "gen-1"} {
		source.SpawnGeneration = generation
		source.OriginalMessageID, source.PreviousMessageID = original.ID.String(), predecessor
		body, _ := json.Marshal(source)
		delivery, err := store.Send(
			recipient,
			RedeliveryKey(root, "task-1", generation, original.ID.String()),
			string(body),
		)
		if err != nil {
			t.Fatal(err)
		}
		if generation == "gen-1" {
			if _, err := ReadAssignment(store, root, "task-1", delivery.ID); err == nil {
				t.Fatal("accepted cyclic assignment generations")
			}
		}
		predecessor = delivery.ID.String()
	}
}
