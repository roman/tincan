package primary

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type Status string

const (
	Owner  Status = "owner"
	Held   Status = "held_by_other"
	Stale  Status = "stale"
	Vacant Status = "vacant"
)

func validateRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("Concierge root must be an absolute, clean path")
	}
	return nil
}

func withLock(root string, action func() (Status, error)) (Status, error) {
	if err := validateRoot(root); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(root, "primary.claim.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	return action()
}

func readOwner(root string) (Identity, error) {
	path := filepath.Join(root, "primary.json")
	info, err := os.Lstat(path)
	if err != nil {
		return Identity{}, err
	}
	if !info.Mode().IsRegular() {
		return Identity{}, errors.New("primary owner record is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, err
	}
	var identity Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return Identity{}, err
	}
	if identity.Version == 0 && identity.PIDNamespace == "" {
		if err := identity.validateCore(); err != nil {
			return Identity{}, err
		}
	} else if err := identity.validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func sameLegacyOwner(legacy, identity Identity) bool {
	return legacy.Version == 0 &&
		legacy.Kind == identity.Kind &&
		legacy.SessionID == identity.SessionID &&
		legacy.PID == identity.PID &&
		legacy.StartTicks == identity.StartTicks &&
		legacy.BootID == identity.BootID
}

func writeOwner(root string, identity Identity) error {
	if err := identity.validate(); err != nil {
		return err
	}
	file, err := os.CreateTemp(root, ".primary-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(identity); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(root, "primary.json"))
}

func ownerLive(identity Identity) (bool, error) {
	bootID, err := readBootID()
	if err != nil {
		return false, err
	}
	pidNamespace, err := readPIDNamespace()
	if err != nil {
		return false, err
	}
	return checkOwnerLive(identity, bootID, pidNamespace, readProcess)
}

func checkOwnerLive(
	identity Identity,
	bootID string,
	pidNamespace string,
	lookup func(int) (Process, error),
) (bool, error) {
	if bootID != identity.BootID {
		return false, nil
	}
	if pidNamespace != identity.PIDNamespace {
		return true, nil
	}
	process, err := lookup(identity.PID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sameLiveProcess(identity, bootID, process), nil
}

func Release(root string, identity Identity) (Status, error) {
	if err := identity.validate(); err != nil {
		return "", err
	}
	return withLock(root, func() (Status, error) {
		current, err := readOwner(root)
		if errors.Is(err, os.ErrNotExist) {
			return Vacant, nil
		}
		if err != nil {
			return "", fmt.Errorf("read primary owner: %w", err)
		}
		if current != identity && !sameLegacyOwner(current, identity) {
			return Held, nil
		}
		if err := os.Remove(filepath.Join(root, "primary.json")); err != nil {
			return "", err
		}
		return Vacant, nil
	})
}

func sameLiveProcess(identity Identity, bootID string, process Process) bool {
	return bootID == identity.BootID &&
		process.PID == identity.PID &&
		process.StartTicks == identity.StartTicks &&
		process.Kind == identity.Kind
}

func Claim(root string, identity Identity) (Status, error) {
	return claimWithProbe(root, identity, ownerLive)
}

func claimWithProbe(
	root string,
	identity Identity,
	liveProbe func(Identity) (bool, error),
) (Status, error) {
	if err := identity.validate(); err != nil {
		return "", err
	}
	return withLock(root, func() (Status, error) {
		current, err := readOwner(root)
		if errors.Is(err, os.ErrNotExist) {
			return Owner, writeOwner(root, identity)
		}
		if err != nil {
			return "", fmt.Errorf("read primary owner: %w", err)
		}
		if current.Version == 0 {
			if sameLegacyOwner(current, identity) {
				return Owner, writeOwner(root, identity)
			}
			return Held, nil
		}
		live, err := liveProbe(current)
		if err != nil {
			return "", err
		}
		if !live {
			return Owner, writeOwner(root, identity)
		}
		if current == identity {
			return Owner, nil
		}
		return Held, nil
	})
}

func Verify(root string, identity Identity) (Status, error) {
	return verifyWithProbe(root, identity, ownerLive)
}

func verifyWithProbe(
	root string,
	identity Identity,
	liveProbe func(Identity) (bool, error),
) (Status, error) {
	if err := identity.validate(); err != nil {
		return "", err
	}
	return withLock(root, func() (Status, error) {
		current, err := readOwner(root)
		if errors.Is(err, os.ErrNotExist) {
			return Vacant, nil
		}
		if err != nil {
			return "", fmt.Errorf("read primary owner: %w", err)
		}
		if current.Version == 0 {
			if sameLegacyOwner(current, identity) {
				return Owner, writeOwner(root, identity)
			}
			return Held, nil
		}
		live, err := liveProbe(current)
		if err != nil {
			return "", err
		}
		if !live {
			return Stale, nil
		}
		if current == identity {
			return Owner, nil
		}
		return Held, nil
	})
}
