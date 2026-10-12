package taskstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"

	"github.com/roman/tincan/pkg/stateversion"
)

var initialVersionTemporary = regexp.MustCompile(`^\.task-state-version-[A-Z2-7]{26,}$`)

// Initialize prepares fresh version two state or confirms a completed initialization.
// State writers and installers must be stopped while a fresh root is inspected.
func Initialize(ctx context.Context, path string) error {
	return initialize(ctx, path, syncDirectory)
}

func initialize(
	ctx context.Context,
	path string,
	syncPreparedDirectory func(*os.Root) error,
) (result error) {
	guard, state, err := stateversion.AcquireInitialization(ctx, path)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, guard.Close()) }()
	root := guard.StateRoot()
	if state == stateversion.FreshRoot {
		if err := validateFreshRoot(root); err != nil {
			return err
		}
		if err := root.Mkdir("records", 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	records, _, err := openRecords(root)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, records.Close()) }()
	if err := syncPreparedDirectory(records); err != nil {
		return err
	}
	if err := syncPreparedDirectory(root); err != nil {
		return err
	}
	return guard.PublishInitialVersion()
}

func validateFreshRoot(root *os.Root) error {
	entries, err := initializationEntries(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		selected, err := root.Lstat(entry.Name())
		if err != nil {
			return err
		}
		switch entry.Name() {
		case "primary.json", "primary.claim.lock":
			if selected.Mode().IsRegular() {
				continue
			}
		case "profile":
			if selected.Mode()&os.ModeSymlink != 0 {
				continue
			}
		case "records":
			records, _, err := openRecords(root)
			if err != nil {
				return err
			}
			contents, readErr := initializationEntries(records)
			closeErr := records.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return err
			}
			if len(contents) == 0 {
				continue
			}
		default:
			if selected.Mode().IsRegular() && initialVersionTemporary.MatchString(entry.Name()) {
				continue
			}
		}
		return fmt.Errorf(
			"fresh task-state initialization refuses existing entry %q; migrate legacy state instead",
			entry.Name(),
		)
	}
	return nil
}

func initializationEntries(root *os.Root) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return directory.ReadDir(-1)
}
