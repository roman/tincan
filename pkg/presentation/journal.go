package presentation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/roman/tincan/pkg/herdr"
)

type closeJournal struct {
	Session   string `json:"session"`
	Pane      string `json:"pane"`
	Socket    string `json:"socket"`
	Workspace string `json:"workspace"`
	Tab       string `json:"tab"`
	Focus     focus  `json:"focus"`
}

func journalPath(namespace string, target herdr.Target, socket string) string {
	key := sha256.Sum256([]byte(target.Session() + "\x00" + socket + "\x00" + target.PaneID()))
	return filepath.Join(namespace, "focus-"+hex.EncodeToString(key[:])[:32]+".json")
}

func readCloseJournal(
	path string,
	target herdr.Target,
	socket, workspace, tab string,
) (closeJournal, os.FileInfo, error) {
	selected, err := os.Lstat(path)
	if err != nil {
		return closeJournal{}, nil, err
	}
	if !selected.Mode().IsRegular() {
		return closeJournal{}, nil, herdr.ErrResponse
	}
	file, err := os.Open(path)
	if err != nil {
		return closeJournal{}, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(selected, opened) {
		return closeJournal{}, nil, herdr.ErrResponse
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return closeJournal{}, nil, herdr.ErrResponse
	}
	var journal closeJournal
	if json.Unmarshal(data, &journal) != nil || journal.Session != target.Session() ||
		journal.Pane != target.PaneID() || journal.Socket != socket ||
		journal.Workspace != workspace || journal.Tab != tab ||
		journal.Focus.Workspace == "" || journal.Focus.Tab == "" {
		return closeJournal{}, nil, herdr.ErrResponse
	}
	return journal, selected, nil
}

func publishCloseJournal(path string, journal closeJournal) (os.FileInfo, error) {
	data, err := json.Marshal(journal)
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".focus-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(file.Name(), path); err != nil {
		return nil, err
	}
	return info, syncJournalDirectory(filepath.Dir(path))
}

func clearCloseJournal(path string, selected os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(selected, current) {
		return fmt.Errorf("%w: focus journal changed", herdr.ErrResponse)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncJournalDirectory(filepath.Dir(path))
}

func syncJournalDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
