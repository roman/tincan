// Package firstmate reads local Firstmate task metadata for guarded operations.
package firstmate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	ErrInvalidTaskID   = errors.New("invalid Firstmate task ID")
	ErrInvalidPath     = errors.New("invalid absolute path")
	ErrInvalidMetadata = errors.New("invalid Firstmate task metadata")
)

// TaskID is a validated Firstmate task selector.
type TaskID struct{ value string }

// ParseTaskID validates a task ID before it is used to select metadata.
func ParseTaskID(value string) (TaskID, error) {
	if !validCharacters(
		value,
		"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-",
	) {
		return TaskID{}, ErrInvalidTaskID
	}
	return TaskID{value: value}, nil
}

// String returns the task ID.
func (id TaskID) String() string { return id.value }

// AbsolutePath is a validated, clean absolute path from task metadata.
type AbsolutePath struct{ value string }

// ParseAbsolutePath accepts clean absolute paths without control characters.
func ParseAbsolutePath(value string) (AbsolutePath, error) {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || hasControl(value) {
		return AbsolutePath{}, ErrInvalidPath
	}
	return AbsolutePath{value: value}, nil
}

// String returns the absolute path.
func (path AbsolutePath) String() string { return path.value }

// Metadata describes one validated local Herdr task record.
type Metadata struct {
	id              TaskID
	project         AbsolutePath
	worktree        AbsolutePath
	window          string
	herdrSession    string
	herdrWorkspace  string
	herdrTab        string
	herdrPane       string
	harness         string
	kind            string
	spawnGeneration string
}

// TaskID returns the task identity.
func (metadata Metadata) TaskID() TaskID { return metadata.id }

// Project returns the recorded project path.
func (metadata Metadata) Project() AbsolutePath { return metadata.project }

// Worktree returns the recorded worktree path.
func (metadata Metadata) Worktree() AbsolutePath { return metadata.worktree }

// Window returns the Herdr session and pane target.
func (metadata Metadata) Window() string { return metadata.window }

// HerdrSession returns the recorded Herdr session.
func (metadata Metadata) HerdrSession() string { return metadata.herdrSession }

// HerdrWorkspaceID returns the recorded Herdr workspace ID.
func (metadata Metadata) HerdrWorkspaceID() string { return metadata.herdrWorkspace }

// HerdrTabID returns the recorded Herdr tab ID.
func (metadata Metadata) HerdrTabID() string { return metadata.herdrTab }

// HerdrPaneID returns the recorded Herdr pane ID.
func (metadata Metadata) HerdrPaneID() string { return metadata.herdrPane }

// Harness returns the worker harness.
func (metadata Metadata) Harness() string { return metadata.harness }

// Kind returns the task kind.
func (metadata Metadata) Kind() string { return metadata.kind }

// SpawnGeneration returns the task incarnation token.
func (metadata Metadata) SpawnGeneration() string { return metadata.spawnGeneration }

// ReadTask reads the metadata file selected by a validated state directory and task ID.
// It refuses symlinks and changes to the selected file between inspection and opening.
func ReadTask(stateDir AbsolutePath, expected TaskID) (Metadata, error) {
	if stateDir.value == "" {
		return Metadata{}, ErrInvalidPath
	}
	root, err := os.OpenRoot(stateDir.value)
	if err != nil {
		return Metadata{}, err
	}
	defer root.Close()
	return ReadTaskFromRoot(root, expected)
}

// ReadTaskFromRoot reads metadata through a selected task directory handle.
func ReadTaskFromRoot(root *os.Root, expected TaskID) (Metadata, error) {
	if root == nil {
		return Metadata{}, ErrInvalidPath
	}
	if expected.value == "" {
		return Metadata{}, ErrInvalidTaskID
	}
	name := expected.value + ".meta"
	selected, err := root.Lstat(name)
	if err != nil {
		return Metadata{}, err
	}
	if !selected.Mode().IsRegular() {
		return Metadata{}, fmt.Errorf("%w: metadata file is not regular", ErrInvalidMetadata)
	}
	file, err := root.Open(name)
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Metadata{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(selected, opened) {
		return Metadata{}, fmt.Errorf("%w: metadata file changed during open", ErrInvalidMetadata)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return Metadata{}, err
	}
	return Parse(data, expected)
}

// Parse validates one local Herdr task record against the expected task ID.
// Safety fields must each occur exactly once; unrelated keys are ignored.
func Parse(data []byte, expected TaskID) (Metadata, error) {
	if expected.value == "" {
		return Metadata{}, ErrInvalidTaskID
	}
	values := make(map[string]string)
	counts := make(map[string]int)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[key] = value
		counts[key]++
	}
	required := []string{
		"window", "endpoint_task_id", "worktree", "project", "harness", "kind",
		"spawn_gen", "backend", "herdr_session", "herdr_workspace_id",
		"herdr_tab_id", "herdr_pane_id",
	}
	for _, key := range required {
		if counts[key] != 1 || values[key] == "" || hasControl(values[key]) {
			return Metadata{}, fmt.Errorf("%w: missing or ambiguous %s", ErrInvalidMetadata, key)
		}
	}
	if counts["remote_host"] != 0 {
		return Metadata{}, fmt.Errorf("%w: remote task", ErrInvalidMetadata)
	}
	if values["backend"] != "herdr" || values["endpoint_task_id"] != expected.value {
		return Metadata{}, fmt.Errorf("%w: backend or task binding", ErrInvalidMetadata)
	}
	project, err := ParseAbsolutePath(values["project"])
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: project: %v", ErrInvalidMetadata, err)
	}
	worktree, err := ParseAbsolutePath(values["worktree"])
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: worktree: %v", ErrInvalidMetadata, err)
	}
	session := values["herdr_session"]
	workspace := values["herdr_workspace_id"]
	tab := values["herdr_tab_id"]
	pane := values["herdr_pane_id"]
	const atom = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._@%+-"
	if !validCharacters(session, atom) ||
		!validCharacters(workspace, atom) ||
		!validCharacters(tab, atom+":") ||
		!validCharacters(pane, atom+":") ||
		values["window"] != session+":"+pane ||
		!validCharacters(values["harness"], atom) ||
		!validCharacters(values["kind"], atom) ||
		!validCharacters(values["spawn_gen"], atom) {
		return Metadata{}, fmt.Errorf(
			"%w: malformed Herdr endpoint or task fields",
			ErrInvalidMetadata,
		)
	}
	return Metadata{
		id: expected, project: project, worktree: worktree,
		window: values["window"], herdrSession: session, herdrWorkspace: workspace,
		herdrTab: tab, herdrPane: pane, harness: values["harness"],
		kind: values["kind"], spawnGeneration: values["spawn_gen"],
	}, nil
}

func validCharacters(value, allowed string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune(allowed, character) {
			return false
		}
	}
	return true
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
