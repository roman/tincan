package taskstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/crew"
	"github.com/roman/tincan/pkg/firstmate"
)

type legacyParts struct {
	row        LegacyBacklogRow
	metadata   *firstmate.Metadata
	worker     *crew.Record
	completion *legacyCompletion
	receipt    *legacyReceipt
}

type legacyCompletion struct {
	TaskID          string         `json:"task_id"`
	SpawnGeneration string         `json:"spawn_generation"`
	Session         string         `json:"session"`
	Pane            string         `json:"pane"`
	Mode            CompletionMode `json:"mode"`
}

type legacyReceipt struct {
	TaskID          string         `json:"task_id"`
	SpawnGeneration string         `json:"spawn_generation"`
	Mode            CompletionMode `json:"mode"`
}

// inventoryLegacy converts a guarded version 1 state without changing it.
func inventoryLegacy(root *os.Root) ([]Task, error) {
	backlog, err := readLegacyFile(root, "backlog.md", maximumLegacyBacklogBytes)
	if err != nil {
		return nil, err
	}
	rows, err := ParseLegacyBacklog(backlog)
	if err != nil {
		return nil, err
	}
	parts := make(map[string]*legacyParts, len(rows))
	for _, row := range rows {
		parts[row.Definition.ID] = &legacyParts{row: row}
	}
	if err := collectLegacyFiles(root, "tasks", parts); err != nil {
		return nil, err
	}
	if err := collectLegacyFiles(root, "crew", parts); err != nil {
		return nil, err
	}
	if err := checkLegacyWorkerIdentities(parts); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(rows))
	for _, row := range rows {
		task, err := joinLegacyTask(*parts[row.Definition.ID])
		if err != nil {
			return nil, fmt.Errorf("legacy task %q: %w", row.Definition.ID, err)
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func checkLegacyWorkerIdentities(parts map[string]*legacyParts) error {
	names := map[string]string{}
	panes := map[string]string{}
	for taskID, part := range parts {
		if part.worker == nil {
			continue
		}
		worker := part.worker
		name := worker.HerdrSession + "\x00" + worker.Name
		pane := worker.HerdrSession + "\x00" + worker.PaneID
		if other := names[name]; other != "" && other != taskID {
			return fmt.Errorf("legacy tasks %q and %q share a worker name", other, taskID)
		}
		if other := panes[pane]; other != "" && other != taskID {
			return fmt.Errorf("legacy tasks %q and %q share a worker pane", other, taskID)
		}
		names[name], panes[pane] = taskID, taskID
	}
	return nil
}

func collectLegacyFiles(root *os.Root, directory string, parts map[string]*legacyParts) error {
	selected, err := root.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !selected.IsDir() {
		return fmt.Errorf("legacy %s is not a directory", directory)
	}
	child, err := root.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer child.Close()
	opened, err := child.Stat(".")
	if err != nil || !os.SameFile(selected, opened) {
		return fmt.Errorf("legacy %s changed during open", directory)
	}
	listing, err := child.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := listing.ReadDir(-1)
	closeErr := listing.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if directory == "crew" && name == ".lock" {
			continue
		}
		id, kind, err := legacyFileKind(directory, name)
		if err != nil {
			return err
		}
		part := parts[id]
		if part == nil {
			return fmt.Errorf("orphan legacy %s file %q", kind, name)
		}
		data, err := readLegacyFile(child, name, 1<<20)
		if err != nil {
			return err
		}
		if err := addLegacyFile(part, id, kind, data); err != nil {
			return fmt.Errorf("legacy %s file %q: %w", kind, name, err)
		}
	}
	current, err := root.Lstat(directory)
	if err != nil || !os.SameFile(selected, current) {
		return fmt.Errorf("legacy %s changed during inventory", directory)
	}
	return nil
}

func legacyFileKind(directory, name string) (string, string, error) {
	var id, kind string
	if directory == "crew" && strings.HasSuffix(name, ".json") {
		id, kind = strings.TrimSuffix(name, ".json"), "worker"
	} else if directory == "tasks" {
		for _, suffix := range []struct{ name, kind string }{
			{".completion.json", "completion"}, {".receipt.json", "receipt"}, {".meta", "metadata"},
		} {
			if strings.HasSuffix(name, suffix.name) {
				id, kind = strings.TrimSuffix(name, suffix.name), suffix.kind
				break
			}
		}
	}
	if _, err := firstmate.ParseTaskID(id); err != nil || len(id) > maximumTaskIDBytes {
		return "", "", fmt.Errorf("unsupported legacy %s entry %q", directory, name)
	}
	return id, kind, nil
}

func addLegacyFile(part *legacyParts, id, kind string, data []byte) error {
	switch kind {
	case "metadata":
		if err := checkLegacyMetadataKeys(data); err != nil {
			return err
		}
		taskID, _ := firstmate.ParseTaskID(id)
		metadata, err := firstmate.Parse(data, taskID)
		if err != nil {
			return err
		}
		part.metadata = &metadata
	case "worker":
		var record crew.Record
		if err := decodeLegacyJSON(data, &record); err != nil {
			return fmt.Errorf("invalid worker record: %w", err)
		}
		if record.TaskID != id {
			return fmt.Errorf("worker task ID differs from filename")
		}
		part.worker = &record
	case "completion":
		var marker legacyCompletion
		if err := decodeLegacyJSON(data, &marker); err != nil {
			return fmt.Errorf("invalid completion marker: %w", err)
		}
		if marker.TaskID != id {
			return fmt.Errorf("completion task ID differs from filename")
		}
		part.completion = &marker
	case "receipt":
		var receipt legacyReceipt
		if err := decodeLegacyJSON(data, &receipt); err != nil {
			return fmt.Errorf("invalid receipt: %w", err)
		}
		if receipt.TaskID != id {
			return fmt.Errorf("receipt task ID differs from filename")
		}
		part.receipt = &receipt
	}
	return nil
}

func joinLegacyTask(part legacyParts) (Task, error) {
	snapshot := Snapshot{Definition: part.row.Definition, State: part.row.State}
	if part.metadata != nil && part.metadata.Kind() != snapshot.Definition.Kind {
		return Task{}, ErrInvalidTask
	}
	if part.worker != nil {
		worker := part.worker
		snapshot.Worker = &Worker{
			Name: worker.Name, Harness: worker.Kind, Session: worker.HerdrSession,
			Pane: worker.PaneID, Project: worker.Project, Worktree: worker.Worktree,
			Generation: worker.SpawnGeneration, PendingGeneration: worker.PendingGeneration,
			PendingLaunch: worker.PendingLaunch, PendingAdoption: worker.PendingAdoption,
			AgentArgs: worker.AgentArgs,
		}
	}
	if part.metadata == nil && part.worker != nil && !part.worker.PendingAdoption &&
		part.completion == nil {
		return Task{}, ErrInvalidTask
	}
	if part.metadata != nil && part.worker == nil {
		return Task{}, ErrInvalidTask
	}
	if part.metadata != nil && part.worker != nil {
		metadata, worker := part.metadata, part.worker
		if metadata.Project().String() != worker.Project ||
			metadata.Worktree().String() != worker.Worktree ||
			metadata.Harness() != worker.Kind ||
			metadata.HerdrSession() != worker.HerdrSession ||
			metadata.HerdrPaneID() != worker.PaneID ||
			(metadata.SpawnGeneration() != worker.SpawnGeneration &&
				metadata.SpawnGeneration() != worker.PendingGeneration) {
			return Task{}, ErrInvalidTask
		}
		snapshot.Worker.Workspace = metadata.HerdrWorkspaceID()
		snapshot.Worker.Tab = metadata.HerdrTabID()
	}
	if part.completion != nil {
		marker := part.completion
		phase := Retired
		if snapshot.Worker != nil {
			if part.metadata != nil {
				phase = Accepted
			}
			if marker.SpawnGeneration != snapshot.Worker.Generation ||
				marker.Session != snapshot.Worker.Session || marker.Pane != snapshot.Worker.Pane {
				return Task{}, ErrInvalidTask
			}
		}
		if firstmate.ValidateSpawnGeneration(marker.SpawnGeneration) != nil ||
			(marker.Mode != Close && marker.Mode != Retain) {
			return Task{}, ErrInvalidTask
		}
		if _, err := herdr.ParseTarget(marker.Session, marker.Pane); err != nil {
			return Task{}, ErrInvalidTask
		}
		if phase == Retired &&
			((marker.Mode == Close && snapshot.State != Done) ||
				(marker.Mode == Retain && snapshot.State != Queued)) {
			return Task{}, ErrInvalidTask
		}
		snapshot.Completion = &Completion{
			Generation: marker.SpawnGeneration, Mode: marker.Mode, Phase: phase,
		}
	}
	if part.receipt != nil {
		snapshot.Receipt = &Receipt{
			Generation: part.receipt.SpawnGeneration, Mode: part.receipt.Mode,
		}
	}
	return NewMigrated(snapshot)
}

func readLegacyFile(root *os.Root, name string, limit int64) ([]byte, error) {
	selected, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !selected.Mode().IsRegular() {
		return nil, fmt.Errorf("legacy %s is not a regular file", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(selected, opened) {
		return nil, fmt.Errorf("legacy %s changed during open", name)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("legacy %s exceeds size limit", name)
	}
	return data, nil
}

func checkLegacyMetadataKeys(data []byte) error {
	allowed := map[string]bool{
		"window": true, "endpoint_task_id": true, "worktree": true, "project": true,
		"harness": true, "kind": true, "spawn_gen": true, "backend": true,
		"herdr_session": true, "herdr_workspace_id": true, "herdr_tab_id": true,
		"herdr_pane_id": true,
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		key, _, found := strings.Cut(line, "=")
		if !found || !allowed[key] {
			return fmt.Errorf("unsupported legacy metadata key %q", key)
		}
	}
	return nil
}

func decodeLegacyJSON(data []byte, destination any) error {
	keys := json.NewDecoder(bytes.NewReader(data))
	opening, err := keys.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("legacy JSON must be an object")
	}
	seen := map[string]bool{}
	for keys.More() {
		key, err := keys.Token()
		if err != nil {
			return err
		}
		name := strings.ToLower(key.(string))
		if seen[name] {
			return fmt.Errorf("duplicate legacy JSON field %q", name)
		}
		seen[name] = true
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := keys.Token(); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing legacy JSON data")
	}
	return nil
}
