// Package crew stores the launch identity for local task workers.
package crew

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
)

var (
	ErrInvalidRecord = errors.New("invalid crew record")
	ErrConflict      = errors.New("crew record already has different launch details")
	ErrLockTimeout   = errors.New("crew registry lock timed out")
)

const maximumRecordBytes = 1 << 20
const registryLockTimeout = 2 * time.Second

// Record binds a local task to the exact worker launch details needed for recovery.
type Record struct {
	TaskID          string   `json:"task_id"`
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	PaneID          string   `json:"pane_id"`
	HerdrSession    string   `json:"herdr_session"`
	Project         string   `json:"project"`
	Worktree        string   `json:"worktree"`
	SpawnGeneration string   `json:"spawn_generation"`
	AgentArgs       []string `json:"agent_args"`
}

type Registry struct{ root string }

// New selects an absolute Tincan state root without creating files.
func New(root string) (*Registry, error) {
	path, err := firstmate.ParseAbsolutePath(root)
	if err != nil {
		return nil, err
	}
	return &Registry{root: path.String()}, nil
}

func safeAtom(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune(
			"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._@%+-",
			character,
		) {
			return false
		}
	}
	return true
}

// ValidateLaunch rejects names and arguments that cannot be recovered safely.
func ValidateLaunch(name string, arguments []string) error {
	if !safeAtom(name) || len(arguments) == 0 {
		return ErrInvalidRecord
	}
	for _, argument := range arguments {
		if argument == "" || strings.IndexFunc(argument, unicode.IsControl) >= 0 {
			return ErrInvalidRecord
		}
	}
	return nil
}

func validRecord(record Record) bool {
	if _, err := firstmate.ParseTaskID(record.TaskID); err != nil {
		return false
	}
	if ValidateLaunch(record.Name, record.AgentArgs) != nil || !safeAtom(record.Kind) ||
		!safeAtom(record.SpawnGeneration) {
		return false
	}
	if _, err := herdr.ParseTarget(record.HerdrSession, record.PaneID); err != nil {
		return false
	}
	if _, err := firstmate.ParseAbsolutePath(record.Project); err != nil {
		return false
	}
	if _, err := firstmate.ParseAbsolutePath(record.Worktree); err != nil {
		return false
	}
	return true
}

func syncRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func verifiedRoot(path string) (*os.Root, error) {
	selected, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !selected.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrInvalidRecord, path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	openedInfo, statErr := opened.Stat()
	opened.Close()
	if statErr != nil || !os.SameFile(selected, openedInfo) {
		root.Close()
		return nil, fmt.Errorf("%w: %s changed during open", ErrInvalidRecord, path)
	}
	return root, nil
}

func createDirectory(path string) error {
	err := os.Mkdir(path, 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	parent, err := verifiedRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return syncRoot(parent)
}

func (registry *Registry) ensureDirectories() error {
	if registry == nil || registry.root == "" {
		return ErrInvalidRecord
	}
	if err := createDirectory(registry.root); err != nil {
		return err
	}
	root, err := verifiedRoot(registry.root)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Mkdir("crew", 0o700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return syncRoot(root)
}

func (registry *Registry) openCrew() (*os.Root, error) {
	if registry == nil || registry.root == "" {
		return nil, ErrInvalidRecord
	}
	root, err := verifiedRoot(registry.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	selectedCrew, err := root.Lstat("crew")
	if err != nil {
		return nil, err
	}
	if !selectedCrew.IsDir() {
		return nil, fmt.Errorf("%w: crew is not a directory", ErrInvalidRecord)
	}
	crewRoot, err := root.OpenRoot("crew")
	if err != nil {
		return nil, err
	}
	openedCrew, err := crewRoot.Open(".")
	if err != nil {
		crewRoot.Close()
		return nil, err
	}
	openedCrewInfo, statErr := openedCrew.Stat()
	openedCrew.Close()
	if statErr != nil || !os.SameFile(selectedCrew, openedCrewInfo) {
		crewRoot.Close()
		return nil, fmt.Errorf("%w: crew directory changed during open", ErrInvalidRecord)
	}
	return crewRoot, nil
}

func readRecord(root *os.Root, taskID string) (Record, error) {
	name := taskID + ".json"
	selected, err := root.Lstat(name)
	if err != nil {
		return Record{}, err
	}
	if !selected.Mode().IsRegular() {
		return Record{}, fmt.Errorf("%w: record is not a regular file", ErrInvalidRecord)
	}
	file, err := root.Open(name)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Record{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(selected, opened) {
		return Record{}, fmt.Errorf("%w: record changed during open", ErrInvalidRecord)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumRecordBytes+1))
	if err != nil {
		return Record{}, err
	}
	if len(data) > maximumRecordBytes {
		return Record{}, fmt.Errorf("%w: record exceeds 1 MiB", ErrInvalidRecord)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil || !validRecord(record) ||
		record.TaskID != taskID {
		return Record{}, fmt.Errorf("%w: malformed record for %s", ErrInvalidRecord, taskID)
	}
	return record, nil
}

func listRecords(root *os.Root) ([]Record, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		taskID := strings.TrimSuffix(entry.Name(), ".json")
		if _, err := firstmate.ParseTaskID(taskID); err != nil {
			return nil, fmt.Errorf("%w: invalid filename %s", ErrInvalidRecord, entry.Name())
		}
		record, err := readRecord(root, taskID)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(
		records,
		func(left, right int) bool { return records[left].TaskID < records[right].TaskID },
	)
	return records, nil
}

func temporaryFile(root *os.Root) (*os.File, string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, "", err
	}
	name := ".writing-" + hex.EncodeToString(random)
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	return file, name, err
}

func acquireLock(file *os.File) error {
	deadline := time.Now().Add(registryLockTimeout)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		if time.Now().After(deadline) {
			return ErrLockTimeout
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Register publishes a launch record once. The same record is an idempotent retry.
func (registry *Registry) Register(record Record) error {
	if !validRecord(record) {
		return ErrInvalidRecord
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maximumRecordBytes {
		return fmt.Errorf("%w: record exceeds 1 MiB", ErrInvalidRecord)
	}
	if err := registry.ensureDirectories(); err != nil {
		return err
	}
	root, err := registry.openCrew()
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := acquireLock(lock); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	records, err := listRecords(root)
	if err != nil {
		return err
	}
	for _, existing := range records {
		if existing.TaskID == record.TaskID {
			if !reflect.DeepEqual(existing, record) {
				return ErrConflict
			}
			return syncRoot(root)
		}
		if existing.HerdrSession == record.HerdrSession &&
			(existing.Name == record.Name || existing.PaneID == record.PaneID) {
			return ErrConflict
		}
	}
	temporary, temporaryName, err := temporaryFile(root)
	if err != nil {
		return err
	}
	defer root.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := root.Link(temporaryName, record.TaskID+".json"); err != nil {
		return err
	}
	return syncRoot(root)
}

// Get reads the registered launch identity for one task.
func (registry *Registry) Get(taskID string) (Record, error) {
	if _, err := firstmate.ParseTaskID(taskID); err != nil {
		return Record{}, ErrInvalidRecord
	}
	root, err := registry.openCrew()
	if err != nil {
		return Record{}, err
	}
	defer root.Close()
	return readRecord(root, taskID)
}

// List reads all registered crew in task ID order.
func (registry *Registry) List() ([]Record, error) {
	root, err := registry.openCrew()
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return listRecords(root)
}
