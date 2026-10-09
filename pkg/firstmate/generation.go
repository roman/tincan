package firstmate

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// CreateTaskMetadataLocked publishes a validated local task without replacing an existing record.
// The caller must hold the task metadata lock for the full operation.
// Published is true after the link, including when the directory sync fails.
func CreateTaskMetadataLocked(root *os.Root, id TaskID, data []byte) (published bool, err error) {
	if root == nil {
		return false, ErrInvalidPath
	}
	if _, err := Parse(data, id); err != nil {
		return false, err
	}
	name := id.value + ".meta"
	temporaryName, err := stageTaskMetadata(root, name, data)
	if err != nil {
		return false, err
	}
	defer root.Remove(temporaryName)
	if err := root.Link(temporaryName, name); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return false, err
		}
		existing, readErr := readTaskDataFromRoot(root, id)
		if readErr != nil {
			return false, readErr
		}
		if bytes.Equal(existing, data) {
			return false, nil
		}
		return false, ErrMetadataConflict
	}
	if err := root.Remove(temporaryName); err != nil {
		return true, err
	}
	return true, syncTaskDirectory(root)
}

// ReplaceSpawnGenerationLocked atomically changes a task incarnation.
// The caller must hold the task metadata lock for the full operation.
// Published is true after rename, including when the directory sync fails.
func ReplaceSpawnGenerationLocked(
	root *os.Root,
	id TaskID,
	expected string,
	next string,
) (published bool, err error) {
	if !validSpawnGeneration(expected) || !validSpawnGeneration(next) || expected == next {
		return false, ErrInvalidMetadata
	}
	data, err := readTaskDataFromRoot(root, id)
	if err != nil {
		return false, err
	}
	metadata, err := Parse(data, id)
	if err != nil {
		return false, err
	}
	if metadata.SpawnGeneration() != expected {
		return false, fmt.Errorf("%w: spawn generation changed", ErrInvalidMetadata)
	}
	lines := strings.SplitAfter(string(data), "\n")
	for index, line := range lines {
		if strings.HasPrefix(line, "spawn_gen=") {
			ending := ""
			if strings.HasSuffix(line, "\n") {
				ending = "\n"
			}
			lines[index] = "spawn_gen=" + next + ending
			break
		}
	}
	return publishTaskMetadata(root, id.value+".meta", []byte(strings.Join(lines, "")))
}

func publishTaskMetadata(root *os.Root, name string, data []byte) (bool, error) {
	temporaryName, err := stageTaskMetadata(root, name, data)
	if err != nil {
		return false, err
	}
	defer root.Remove(temporaryName)
	if err := root.Rename(temporaryName, name); err != nil {
		return false, err
	}
	return true, syncTaskDirectory(root)
}

func stageTaskMetadata(root *os.Root, name string, data []byte) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	temporaryName := "." + name + ".writing-" + hex.EncodeToString(random)
	temporary, err := root.OpenFile(temporaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = root.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	committed = true
	return temporaryName, nil
}

func syncTaskDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
