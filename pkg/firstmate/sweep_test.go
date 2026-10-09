package firstmate

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type unknownTypeEntry struct{ os.DirEntry }

func (entry unknownTypeEntry) Type() fs.FileMode { return 0 }

func TestUnknownTypeMetadataUsesFileInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.meta")
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	regular, err := regularMetadataEntry(unknownTypeEntry{entries[0]})
	if err != nil || regular {
		t.Fatalf("regular=%t error=%v", regular, err)
	}
}

func TestSweepContinuesAfterTaskFailure(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"task-1.meta", "task-2.meta"} {
		if err := os.WriteFile(filepath.Join(state.String(), name), []byte("metadata"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("task-1.meta", filepath.Join(state.String(), "task-3.meta")); err != nil {
		t.Fatal(err)
	}
	wantFailure := errors.New("task failed")
	called := []string{}
	reports, err := Sweep(
		context.Background(),
		state,
		time.Unix(1000, 0),
		func(_ context.Context, id TaskID, _ time.Time) (CheckResult, error) {
			called = append(called, id.String())
			if id.String() == "task-1" {
				return CheckResult{}, wantFailure
			}
			return CheckResult{Outcome: CheckQuiet}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(called) != 2 || called[0] != "task-1" || called[1] != "task-2" {
		t.Fatalf("called=%v", called)
	}
	if len(reports) != 3 || !errors.Is(reports[0].Err, wantFailure) ||
		reports[1].Result.Outcome != CheckQuiet || reports[2].Err == nil {
		t.Fatalf("reports=%+v", reports)
	}
}

func TestSweepStopsAfterCancellation(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"task-1.meta", "task-2.meta"} {
		if err := os.WriteFile(filepath.Join(state.String(), name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := []string{}
	reports, err := Sweep(
		ctx,
		state,
		time.Now(),
		func(_ context.Context, id TaskID, _ time.Time) (CheckResult, error) {
			called = append(called, id.String())
			cancel()
			return CheckResult{}, nil
		},
	)
	if !errors.Is(err, context.Canceled) || len(reports) != 1 || len(called) != 1 {
		t.Fatalf("reports=%+v called=%v error=%v", reports, called, err)
	}
}
