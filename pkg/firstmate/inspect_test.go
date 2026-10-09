package firstmate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func snapshotFiles(t *testing.T, directory string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name != "." {
				files[name+"/"] = ""
			}
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[name] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestInspectRecordTracksPendingAndHandledWithoutWriting(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseTaskID("task-1")
	record, _ := ParseRecordName("001.msg")
	inbox := filepath.Join(state.String(), "task-1.inbox")
	if err := os.MkdirAll(filepath.Join(inbox, "handled"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(inbox, record.String())
	if err := os.WriteFile(path, []byte("schema=fm-task-inbox.v1\n--\nhello"), 0600); err != nil {
		t.Fatal(err)
	}
	ringState := filepath.Join(inbox, ".ring-state")
	if err := os.WriteFile(ringState, []byte("001.msg\t2\t1000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := snapshotFiles(t, inbox)
	pending, err := InspectRecord(state, id, record)
	if err != nil || pending.State != RecordPending || string(pending.Body) != "hello" {
		t.Fatalf("pending=%+v error=%v", pending, err)
	}
	if after := snapshotFiles(t, inbox); !reflect.DeepEqual(before, after) {
		t.Fatalf("pending inspection changed files: before=%v after=%v", before, after)
	}
	if err := os.Rename(path, filepath.Join(inbox, "handled", record.String())); err != nil {
		t.Fatal(err)
	}
	before = snapshotFiles(t, inbox)
	handled, err := InspectRecord(state, id, record)
	if err != nil || handled.State != RecordHandled {
		t.Fatalf("handled=%+v error=%v", handled, err)
	}
	if after := snapshotFiles(t, inbox); !reflect.DeepEqual(before, after) {
		t.Fatalf("handled inspection changed files: before=%v after=%v", before, after)
	}
	stateBytes, err := os.ReadFile(ringState)
	if err != nil || string(stateBytes) != "001.msg\t2\t1000\n" {
		t.Fatalf("retry state=%q error=%v", stateBytes, err)
	}
}

func TestInspectRecordBoundsDefaultPreview(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseTaskID("task-1")
	record, _ := ParseRecordName("001.msg")
	inbox := filepath.Join(state.String(), "task-1.inbox")
	if err := os.Mkdir(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	message := "schema=fm-task-inbox.v1\n--\n" + strings.Repeat("x", 1000)
	if err := os.WriteFile(filepath.Join(inbox, record.String()), []byte(message), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := InspectRecord(state, id, record)
	if err != nil || len(preview.Body) != 800 || preview.BodyCharacters != 1000 ||
		!preview.Truncated {
		t.Fatalf("preview=%+v error=%v", preview, err)
	}
}

func TestInspectRecordRejectsMissingEnvelopeDelimiter(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseTaskID("task-1")
	record, _ := ParseRecordName("001.msg")
	inbox := filepath.Join(state.String(), "task-1.inbox")
	if err := os.Mkdir(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, record.String()), []byte("schema=fm-task-inbox.v1\nhello"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRecord(state, id, record); err == nil {
		t.Fatal("missing delimiter was accepted")
	}
}

func TestInspectRecordRejectsSymlink(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseTaskID("task-1")
	record, _ := ParseRecordName("001.msg")
	inbox := filepath.Join(state.String(), "task-1.inbox")
	if err := os.Mkdir(inbox, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret", filepath.Join(inbox, record.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRecord(state, id, record); err == nil {
		t.Fatal("symlinked record was accepted")
	}
}

func TestInspectRecordTreatsSameFileInBothLocationsAsHandled(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ParseTaskID("task-1")
	record, _ := ParseRecordName("001.msg")
	inbox := filepath.Join(state.String(), "task-1.inbox")
	if err := os.MkdirAll(filepath.Join(inbox, "handled"), 0700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(inbox, record.String())
	if err := os.WriteFile(pending, []byte("schema=fm-task-inbox.v1\n--\nhello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(pending, filepath.Join(inbox, "handled", record.String())); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectRecord(state, id, record)
	if err != nil || inspection.State != RecordHandled {
		t.Fatalf("inspection=%+v error=%v", inspection, err)
	}
}
