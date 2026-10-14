package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/taskstate"
)

func TestLifecycleNeverFallsBackToLocalWrites(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		for _, args := range [][]string{
			{"backlog", "start", "task-1"},
			{"backlog", "hold", "task-1", "--reason", "Wait"},
			{"backlog", "release", "task-1"},
			{"backlog", "release-worktree", "task-1", "--generation", "gen-1"},
			{"backlog", "finish", "task-1", "--generation", "gen-1", "--closed-on", "2026-10-12"},
		} {
			t.Run(mode+"/"+args[1], func(t *testing.T) {
				root := shortTaskRoot(t)
				worker := taskstate.Worker{
					Name:       "worker",
					Harness:    "codex",
					Session:    "work",
					Workspace:  "workspace-1",
					Tab:        "tab-1",
					Pane:       "pane-1",
					Project:    "/repo",
					Worktree:   "/worktree",
					Generation: "gen-1",
					AgentArgs:  []string{"resume", "thread-1"},
				}
				state := taskstate.Queued
				if args[1] == "finish" {
					state = taskstate.InFlight
				}
				seedViewTask(t, root, "task-1", state, &worker, nil)
				if args[1] == "release" {
					store, _ := taskstate.NewStore(root)
					if _, err := store.Update(context.Background(), "task-1", func(task taskstate.Task) (taskstate.Task, error) {
						return task.PlaceHold(taskstate.Hold{Reason: "Wait"})
					}); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(root, "records", "task-1.json")
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "unauthorized" {
					serveTaskRoot(t, root)
				}
				var output bytes.Buffer
				code := Run(
					context.Background(),
					append([]string{"--root", root}, args...),
					nil,
					&output,
					io.Discard,
				)
				if code != 1 {
					t.Fatalf("unadmitted mutation code %d: %s", code, &output)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("unadmitted mutation changed record: %v", err)
				}
			})
		}
	}
}

func TestCleanupRequiresHostAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			root := shortTaskRoot(t)
			worker := taskstate.Worker{
				Name:       "worker",
				Harness:    "codex",
				Session:    "work",
				Workspace:  "workspace-1",
				Tab:        "tab-1",
				Pane:       "pane-1",
				Project:    "/repo",
				Worktree:   "/worktree",
				Generation: "gen-1",
				AgentArgs:  []string{"resume", "thread-1"},
			}
			completion := taskstate.Completion{
				Generation: "gen-1",
				Mode:       taskstate.Close,
				Phase:      taskstate.Accepted,
			}
			seedViewTask(t, root, "task-1", taskstate.Done, &worker, &completion)
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "herdr"), []byte("#!/bin/sh\nprintf '%s\\n' '{\"error\":{\"code\":\"pane_not_found\"}}'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory)
			if mode == "unauthorized" {
				serveTaskRoot(t, root)
			}
			path := filepath.Join(root, "records", "task-1.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			code := Run(
				context.Background(),
				[]string{"--root", root, "backlog", "cleanup", "task-1", "--generation", "gen-1"},
				nil,
				&output,
				io.Discard,
			)
			if code != 1 {
				t.Fatalf("unadmitted cleanup code %d output %s", code, &output)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("unadmitted cleanup changed task: %v", err)
			}
		})
	}
}
