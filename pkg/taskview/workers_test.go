package taskview

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func viewTask(
	t *testing.T,
	id string,
	state taskstate.State,
	worker *taskstate.Worker,
	completion *taskstate.Completion,
) taskstate.Task {
	t.Helper()
	task, err := taskstate.NewMigrated(taskstate.Snapshot{
		Definition: taskstate.Definition{ID: id, Title: "Review", Repo: "repo", Kind: "audit"},
		State:      state, Worker: worker, Completion: completion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func viewWorker() *taskstate.Worker {
	return &taskstate.Worker{Name: "true", Harness: "codex", Session: "null", Pane: "pane-1",
		Workspace: "workspace-1", Tab: "tab:1", Project: "/repo", Worktree: "/repo/work,tree",
		Generation: "gen-1", AgentArgs: []string{"resume", "thread,1"}}
}

func TestWorkerViewsKeepRecoveryStates(t *testing.T) {
	pending := viewWorker()
	pending.PendingAdoption = true
	pending.Workspace = ""
	pending.Tab = ""
	restarting := viewWorker()
	restarting.PendingGeneration = "gen-2"
	restarting.PendingLaunch = "gen-2"
	tasks := []taskstate.Task{
		viewTask(t, "queued", taskstate.Queued, viewWorker(), nil),
		viewTask(t, "adopting", taskstate.Queued, pending, nil),
		viewTask(t, "restarting", taskstate.InFlight, restarting, nil),
		viewTask(
			t,
			"accepted",
			taskstate.InFlight,
			viewWorker(),
			&taskstate.Completion{
				Generation: "gen-1",
				Mode:       taskstate.Close,
				Phase:      taskstate.Accepted,
			},
		),
		viewTask(
			t,
			"retired",
			taskstate.InFlight,
			nil,
			&taskstate.Completion{
				Generation: "gen-1",
				Mode:       taskstate.Close,
				Phase:      taskstate.Retired,
			},
		),
		viewTask(t, "unassigned", taskstate.Queued, nil, nil),
	}
	var workers, local bytes.Buffer
	if err := WriteWorkers(&workers, tasks); err != nil {
		t.Fatal(err)
	}
	wantWorkers := "count: 3\nworkers[3]{task,name,session,pane_id}:\n" +
		"  \"queued\",\"true\",\"null\",\"pane-1\"\n" +
		"  \"restarting\",\"true\",\"null\",\"pane-1\"\n" +
		"  \"accepted\",\"true\",\"null\",\"pane-1\"\n" +
		"pending_adoptions[1]{task,name,session,pane_id}:\n  \"adopting\",\"true\",\"null\",\"pane-1\"\n"
	if workers.String() != wantWorkers {
		t.Fatalf("workers = %q", workers.String())
	}
	if err := WriteLocalTasks(&local, tasks); err != nil {
		t.Fatal(err)
	}
	wantLocal := "count: 3\ntasks[3]{task,kind,worktree}:\n" +
		"  \"queued\",\"audit\",\"/repo/work,tree\"\n" +
		"  \"restarting\",\"audit\",\"/repo/work,tree\"\n" +
		"  \"accepted\",\"audit\",\"/repo/work,tree\"\n"
	if local.String() != wantLocal {
		t.Fatalf("local = %q", local.String())
	}
}

func TestWorkerDetailShowsExactRecoveryBinding(t *testing.T) {
	for _, trial := range []struct {
		name   string
		worker *taskstate.Worker
		want   string
	}{
		{"confirmed", viewWorker(), "task: task-1\nname: true\nkind: codex\npane_id: pane-1\nherdr_session: null\nworkspace: workspace-1\ntab: tab:1\nproject: \"/repo\"\nworktree: \"/repo/work,tree\"\nspawn_generation: gen-1\nagent_args[2]:\n  \"resume\"\n  \"thread,1\"\n"},
		{"restart", func() *taskstate.Worker {
			w := viewWorker()
			w.PendingGeneration = "gen-2"
			w.PendingLaunch = "gen-2"
			return w
		}(), "pending_generation: gen-2\npending_launch: gen-2\n"},
		{"adoption", func() *taskstate.Worker {
			w := viewWorker()
			w.PendingAdoption = true
			w.Workspace = ""
			w.Tab = ""
			return w
		}(), "adoption: pending\n"},
	} {
		t.Run(trial.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := WriteWorker(&output, viewTask(t, "task-1", taskstate.Queued, trial.worker, nil)); err != nil {
				t.Fatal(err)
			}
			if trial.name == "confirmed" {
				if output.String() != trial.want {
					t.Fatalf("detail = %q", output.String())
				}
			} else if !strings.Contains(output.String(), trial.want) {
				t.Fatalf("detail = %q", output.String())
			}
		})
	}
	var output bytes.Buffer
	if err := WriteWorker(&output, viewTask(t, "none", taskstate.Queued, nil, nil)); err == nil ||
		output.Len() != 0 {
		t.Fatalf("missing binding: %v, %s", err, &output)
	}
}

func TestPendingCompletionsIncludeWorkerlessRecovery(t *testing.T) {
	receiptTask, err := taskstate.NewMigrated(
		taskstate.Snapshot{
			Definition: taskstate.Definition{
				ID:    "receipt",
				Title: "Finished",
				Repo:  "repo",
				Kind:  "audit",
			},
			State:   taskstate.Queued,
			Receipt: &taskstate.Receipt{Generation: "gen-old", Mode: taskstate.Retain},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	tasks := []taskstate.Task{
		viewTask(
			t,
			"accepted",
			taskstate.InFlight,
			viewWorker(),
			&taskstate.Completion{
				Generation: "gen-1",
				Mode:       taskstate.Close,
				Phase:      taskstate.Accepted,
			},
		),
		viewTask(
			t,
			"retired",
			taskstate.InFlight,
			nil,
			&taskstate.Completion{
				Generation: "gen-2",
				Mode:       taskstate.Retain,
				Phase:      taskstate.Retired,
			},
		),
		receiptTask,
	}
	var output bytes.Buffer
	if err := WritePendingCompletions(&output, tasks); err != nil {
		t.Fatal(err)
	}
	want := "count: 2\npending_completions[2]{task,mode,generation,phase}:\n" +
		"  \"accepted\",\"close\",\"gen-1\",\"accepted\"\n" +
		"  \"retired\",\"retain\",\"gen-2\",\"retired\"\n"
	if output.String() != want {
		t.Fatalf("pending = %q", output.String())
	}
}

type rejectingWriter struct{}

func (rejectingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWorkerViewsReportWriterFailures(t *testing.T) {
	task := viewTask(
		t,
		"task-1",
		taskstate.InFlight,
		viewWorker(),
		&taskstate.Completion{
			Generation: "gen-1",
			Mode:       taskstate.Close,
			Phase:      taskstate.Accepted,
		},
	)
	for _, trial := range []struct {
		name  string
		write func(io.Writer) error
	}{
		{"workers", func(w io.Writer) error { return WriteWorkers(w, []taskstate.Task{task}) }},
		{"worker", func(w io.Writer) error { return WriteWorker(w, task) }},
		{"tasks", func(w io.Writer) error { return WriteLocalTasks(w, []taskstate.Task{task}) }},
		{"pending", func(w io.Writer) error { return WritePendingCompletions(w, []taskstate.Task{task}) }},
	} {
		t.Run(trial.name, func(t *testing.T) {
			if err := trial.write(rejectingWriter{}); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
