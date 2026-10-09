package firstmate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

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
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return false, err
	}
	temporaryName := "." + name + ".writing-" + hex.EncodeToString(random)
	temporary, err := root.OpenFile(temporaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	defer root.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := root.Rename(temporaryName, name); err != nil {
		return false, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return true, err
	}
	defer directory.Close()
	return true, directory.Sync()
}
