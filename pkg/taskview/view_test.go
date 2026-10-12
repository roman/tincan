package taskview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func TestListQuotesTaskValues(t *testing.T) {
	tasks := make([]taskstate.Task, 0, 2)
	for _, definition := range []taskstate.Definition{
		{ID: "task-a", Title: "true", Repo: "null", Kind: "audit"},
		{ID: "task-b", Title: "A: B # C, \"review\"", Repo: "tincan", Kind: "ship"},
	} {
		task, err := taskstate.NewQueued(definition)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	var output bytes.Buffer
	if err := WriteList(&output, tasks, "queued"); err != nil {
		t.Fatal(err)
	}
	want := "count: 2\ntasks[2]{id,state,kind,repo,title}:\n" +
		"  \"task-a\",queued,\"audit\",\"null\",\"true\"\n" +
		"  \"task-b\",queued,\"ship\",\"tincan\",\"A: B # C, \\\"review\\\"\"\n"
	if output.String() != want {
		t.Fatalf("list = %q, want %q", output.String(), want)
	}
}

func TestListSeparatesHeldAndDoneTasks(t *testing.T) {
	tasks := make([]taskstate.Task, 0, 2)
	for _, snapshot := range []taskstate.Snapshot{
		{Definition: taskstate.Definition{ID: "held", Title: "Waiting", Repo: "tincan", Kind: "ship",
			Hold: &taskstate.Hold{Reason: "review"}}, State: taskstate.Queued},
		{Definition: taskstate.Definition{ID: "done", Title: "Finished", Repo: "tincan", Kind: "ship",
			Closed: "2026-10-11", Hold: &taskstate.Hold{Reason: "historical"}}, State: taskstate.Done},
	} {
		task, err := taskstate.NewMigrated(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	for _, trial := range []struct {
		state string
		want  string
	}{
		{state: "queued", want: "count: 0\n"},
		{state: "held", want: "count: 1\ntasks[1]{id,state,kind,repo,title}:\n  \"held\",queued"},
		{state: "done", want: "count: 1\ntasks[1]{id,state,kind,repo,title}:\n  \"done\",done"},
	} {
		var output bytes.Buffer
		if err := WriteList(&output, tasks, trial.state); err != nil ||
			!strings.Contains(output.String(), trial.want) {
			t.Fatalf("list %s = %v, %s", trial.state, err, output.String())
		}
	}
}

func TestTaskShowsCompleteRecord(t *testing.T) {
	priority := 2
	task, err := taskstate.NewMigrated(taskstate.Snapshot{
		Definition: taskstate.Definition{
			ID: "task-a", Title: "true", Body: "first\nsecond", Repo: "tincan", Kind: "ship",
			Dependencies: []taskstate.Dependency{{Type: "blocked-by", ID: "task-b"}},
			Hold:         &taskstate.Hold{Reason: "review", Kind: "captain", Until: "2026-10-12"},
			Priority:     &priority, Created: "2026-10-10", Closed: "2026-10-11",
		},
		State: taskstate.Done,
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteTask(&output, task, DetailFull); err != nil {
		t.Fatal(err)
	}
	want := "task:\n" +
		"  id: \"task-a\"\n" +
		"  title: \"true\"\n" +
		"  state: done\n" +
		"  blocked: yes\n" +
		"  blocked_by: \"task-b\"\n" +
		"  held: yes\n" +
		"  hold_reason: \"review\"\n" +
		"  hold_kind: \"captain\"\n" +
		"  hold_until: \"2026-10-12\"\n" +
		"  kind: \"ship\"\n" +
		"  repo: \"tincan\"\n" +
		"  priority: 2\n" +
		"  created: 2026-10-10\n" +
		"  closed: 2026-10-11\n" +
		"  deps: \"blocked-by:task-b\"\n" +
		"  body: \"first\\nsecond\"\n"
	if output.String() != want {
		t.Fatalf("task = %q, want %q", output.String(), want)
	}
}

func TestSummaryTruncatesTitleAndBody(t *testing.T) {
	task, err := taskstate.NewQueued(taskstate.Definition{
		ID: "task-a", Title: strings.Repeat("é", 81), Body: strings.Repeat("x", 400),
		Repo: "tincan", Kind: "ship",
	})
	if err != nil {
		t.Fatal(err)
	}
	var summary bytes.Buffer
	if err := WriteTask(&summary, task, DetailSummary); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.String(), "truncated, 81 chars total") ||
		!strings.Contains(summary.String(), "truncated, 400 chars total") ||
		strings.Contains(summary.String(), strings.Repeat("x", 300)) {
		t.Fatalf("summary = %s", summary.String())
	}
	var full bytes.Buffer
	if err := WriteTask(&full, task, DetailFull); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full.String(), strings.Repeat("x", 400)) ||
		!strings.Contains(full.String(), strings.Repeat("é", 81)) {
		t.Fatalf("full = %s", full.String())
	}
}
