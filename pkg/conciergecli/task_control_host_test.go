package conciergecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/hostservice"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestCancelRequiresHostAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			root := shortTaskRoot(t)
			seedViewTask(t, root, "task-1", taskstate.Queued, nil, nil)
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
				[]string{"--root", root, "task", "cancel", "task-1"},
				nil,
				&output,
				io.Discard,
			)
			if code != 1 {
				t.Fatalf("unadmitted cancellation code %d: %s", code, &output)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("unadmitted cancellation changed record: %v", err)
			}
		})
	}
}

func TestStopRequiresHostAdmissionEvenForReceiptRetry(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			root := lifecycleRoot(t)
			store, _ := taskstate.NewStore(root)
			_, err := store.Update(
				context.Background(),
				"task-1",
				func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.Receipt = &taskstate.Receipt{
						Generation: "old-gen",
						Mode:       taskstate.Retain,
					}
					return taskstate.NewMigrated(snapshot)
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unauthorized" {
				serveTaskRoot(t, root)
			}
			var output bytes.Buffer
			code := Run(
				context.Background(),
				[]string{"--root", root, "task", "stop", "task-1", "--generation", "old-gen"},
				nil,
				&output,
				io.Discard,
			)
			if code != 1 {
				t.Fatalf("unadmitted stop code %d: %s", code, &output)
			}
		})
	}
}

func TestUnadmittedStopNeverCallsHerdr(t *testing.T) {
	for _, mode := range []string{"missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			root := lifecycleRoot(t)
			directory := t.TempDir()
			calls := filepath.Join(directory, "calls")
			executable := filepath.Join(directory, "herdr")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf called >> \"$TEST_CALLS\"\nexit 72\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory)
			t.Setenv("TEST_CALLS", calls)
			if mode == "unauthorized" {
				recipient, _ := inbox.ParseRecipient("firstmate")
				serveConfiguredTaskRoot(
					t,
					hostservice.Config{
						Root:            root,
						MessageRoot:     filepath.Join(filepath.Dir(root), ".tincan"),
						Recipient:       recipient,
						HerdrExecutable: executable,
					},
				)
			}
			before, err := os.ReadFile(filepath.Join(root, "records", "task-1.json"))
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			code := Run(
				context.Background(),
				[]string{"--root", root, "task", "stop", "task-1", "--generation", "gen-1"},
				nil,
				&output,
				io.Discard,
			)
			if code != 1 {
				t.Fatalf("unadmitted stop code %d: %s", code, &output)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatalf("unadmitted stop invoked Herdr: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(root, "records", "task-1.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("unadmitted stop changed record: %v", err)
			}
		})
	}
}
