package taskview

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

func briefOptions(t *testing.T) BriefOptions {
	t.Helper()
	recipient, err := inbox.ParseRecipient("operator")
	if err != nil {
		t.Fatal(err)
	}
	return BriefOptions{
		Limit:       1,
		Recipient:   recipient,
		Root:        "/state's/root",
		MessageRoot: "/messages'root",
		Executable:  "/bin/concierge",
	}
}

func TestBriefShowsEmptyRecordState(t *testing.T) {
	var output bytes.Buffer
	if err := WriteBrief(&output, nil, nil, briefOptions(t)); err != nil {
		t.Fatal(err)
	}
	want := "bin: \"/bin/concierge\"\ndescription: \"Pending primary wakes, queued backlog work, local tasks, and registered workers\"\nwakes: 0 pending for operator\nbacklog: 0 queued\ntasks: 0 found\nworkers: 0 registered\n"
	if output.String() != want {
		t.Fatalf("empty brief = %q", output.String())
	}
}

func TestBriefLimitsEverySectionAndKeepsTotals(t *testing.T) {
	var tasks []taskstate.Task
	var messages []inbox.Message
	options := briefOptions(t)
	for _, id := range []string{"first", "second"} {
		tasks = append(tasks, viewTask(t, id, taskstate.Queued, nil, nil))
		tasks = append(
			tasks,
			viewTask(
				t,
				id+"-worker",
				taskstate.InFlight,
				viewWorker(),
				&taskstate.Completion{
					Generation: "gen-1",
					Mode:       taskstate.Close,
					Phase:      taskstate.Accepted,
				},
			),
		)
		worker := viewWorker()
		worker.PendingAdoption = true
		worker.Workspace = ""
		worker.Tab = ""
		tasks = append(tasks, viewTask(t, id+"-adoption", taskstate.Queued, worker, nil))
		messageID, err := inbox.ParseMessageID("k-" + strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		messages = append(
			messages,
			inbox.Message{
				ID:        messageID,
				Recipient: options.Recipient,
				Body:      strings.Repeat("é", 121) + ",<done>",
			},
		)
	}
	held, err := taskstate.NewMigrated(
		taskstate.Snapshot{
			Definition: taskstate.Definition{
				ID:    "held",
				Title: "Hold",
				Repo:  "repo",
				Kind:  "audit",
				Hold:  &taskstate.Hold{Reason: "wait"},
			},
			State: taskstate.Queued,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	tasks = append(tasks, held)
	var output bytes.Buffer
	if err := WriteBrief(&output, tasks, messages, options); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"wakes: 1 of 2 pending for operator\n", "backlog: 1 of 4 queued\n", "tasks: 1 of 2 found\n", "workers: 1 of 2 registered\n", "pending_adoptions: 1 of 2\n", "pending_completions: 1 of 2\n",
		"backlog[1]{id,state,kind,repo,title}:\n  \"first\",queued,\"audit\",\"repo\",\"Review\"\n",
		"pending_completions[1]{task,mode,generation,phase}:\n  \"first-worker\",\"close\",\"gen-1\",\"accepted\"\n",
		strings.Repeat("é", 120) + "… (128 chars total)",
		quote("tincan --root '/messages'\\''root' inbox read operator <id>"),
		quote("tincan --root '/messages'\\''root' inbox list operator"),
		quote("concierge --root '/state'\\''s/root' backlog list"),
		quote("concierge --root '/state'\\''s/root' task list"),
		quote("concierge --root '/state'\\''s/root' worker show <task-id>"),
		quote("concierge --root '/state'\\''s/root' worker list"),
		quote("concierge --root '/state'\\''s/root' task pending"),
		quote("concierge --root '/state'\\''s/root' backlog finish --help"),
		quote("concierge --root '/state'\\''s/root' task stop --help"),
		quote("concierge --root '/state'\\''s/root' backlog cleanup --help"),
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in %s", expected, &output)
		}
	}
	if strings.Contains(output.String(), "second") || strings.Contains(output.String(), "held\"") ||
		strings.Contains(output.String(), "<done>") {
		t.Fatalf("hidden rows or preview: %s", &output)
	}
}

func TestBriefShowsWorkerlessCompletionAndFullWake(t *testing.T) {
	options := briefOptions(t)
	id, err := inbox.ParseMessageID("k-" + strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	task := viewTask(
		t,
		"retired",
		taskstate.InFlight,
		nil,
		&taskstate.Completion{
			Generation: "gen-1",
			Mode:       taskstate.Retain,
			Phase:      taskstate.Retired,
		},
	)
	var output bytes.Buffer
	if err := WriteBrief(&output, []taskstate.Task{task}, []inbox.Message{{ID: id, Recipient: options.Recipient, Body: "line one\nline two, \"quoted\""}}, options); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"wakes: 1 pending for operator\n", quote("line one\nline two, \"quoted\""), "tasks: 0 found\nworkers: 0 registered\n", "pending_completions: 1 of 1\n", "\"retired\",\"retain\",\"gen-1\",\"retired\""} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q: %s", expected, &output)
		}
	}
	if strings.Contains(output.String(), "pending_adoptions") ||
		strings.Contains(output.String(), "inbox list") ||
		strings.Contains(output.String(), "task pending") {
		t.Fatalf("unneeded section or hint: %s", &output)
	}
}

func TestBriefRejectsInvalidOptionsBeforeOutput(t *testing.T) {
	for _, trial := range []struct {
		name   string
		change func(*BriefOptions)
	}{
		{"zero limit", func(o *BriefOptions) { o.Limit = 0 }}, {"negative limit", func(o *BriefOptions) { o.Limit = -1 }}, {"large limit", func(o *BriefOptions) { o.Limit = 101 }}, {"empty recipient", func(o *BriefOptions) { o.Recipient = inbox.Recipient{} }},
	} {
		t.Run(trial.name, func(t *testing.T) {
			options := briefOptions(t)
			trial.change(&options)
			var output bytes.Buffer
			if err := WriteBrief(&output, nil, nil, options); err == nil || output.Len() != 0 {
				t.Fatalf("invalid options: %v, %s", err, &output)
			}
		})
	}
	if err := WriteBrief(rejectingWriter{}, nil, nil, briefOptions(t)); !errors.Is(
		err,
		io.ErrClosedPipe,
	) {
		t.Fatalf("writer error: %v", err)
	}
}

func TestBriefKeepsBlockedQueuedWorkerInBacklog(t *testing.T) {
	snapshot := viewTask(t, "blocked", taskstate.Queued, viewWorker(), nil).Snapshot()
	snapshot.Definition.Title = "Review, \"startup\""
	snapshot.Definition.Dependencies = []taskstate.Dependency{
		{Type: "blocked-by", ID: "dependency"},
	}
	blocked, err := taskstate.NewMigrated(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Definition.ID = "held"
	snapshot.Definition.Hold = &taskstate.Hold{Reason: "wait"}
	snapshot.Worker = nil
	held, err := taskstate.NewMigrated(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteBrief(&output, []taskstate.Task{blocked, held}, nil, briefOptions(t)); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"backlog: 1 queued\n", "backlog[1]{id,state,kind,repo,title}:\n  \"blocked\",queued,\"audit\",\"repo\",\"Review, \\\"startup\\\"\"\n", "tasks: 1 found\n", "workers: 1 registered\n"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %q in %s", expected, &output)
		}
	}
	if strings.Contains(output.String(), "\"held\"") {
		t.Fatalf("held task in brief: %s", &output)
	}
}
