package primary

import (
	"errors"
	"fmt"
	"os"
)

var ErrUnauthorizedCaller = errors.New("caller does not own the primary session")

// Caller contains kernel credentials obtained by the host listener. Request
// payloads and inherited environment variables must never supply these values.
type Caller struct {
	PID int
	UID uint32
}

// AuthorizeCaller admits one operation against the current primary claim.
// Admission completes under the claim lock; external operations run afterward.
func AuthorizeCaller(root string, caller Caller) error {
	if caller.UID != uint32(os.Geteuid()) || caller.PID < 1 {
		return ErrUnauthorizedCaller
	}
	bootID, err := readBootID()
	if err != nil {
		return err
	}
	namespace, err := readPIDNamespace()
	if err != nil {
		return err
	}
	_, err = withLock(root, func() (Status, error) {
		owner, err := readOwner(root)
		if err != nil {
			return Held, fmt.Errorf("%w: read primary claim: %v", ErrUnauthorizedCaller, err)
		}
		if err := authorizeCallerIdentity(owner, caller, uint32(os.Geteuid()), bootID, namespace, readProcess); err != nil {
			return Held, err
		}
		return Owner, nil
	})
	return err
}

func authorizeCallerIdentity(
	owner Identity,
	caller Caller,
	uid uint32,
	bootID string,
	namespace string,
	lookup func(int) (Process, error),
) error {
	if caller.UID != uid || caller.PID < 1 || owner.validate() != nil ||
		owner.BootID != bootID || owner.PIDNamespace != namespace {
		return ErrUnauthorizedCaller
	}
	live, err := lookup(owner.PID)
	if err != nil || !sameLiveProcess(owner, bootID, live) {
		return ErrUnauthorizedCaller
	}
	pid := caller.PID
	for range 64 {
		if pid < 1 {
			break
		}
		process, err := lookup(pid)
		if err != nil {
			return ErrUnauthorizedCaller
		}
		if process.Kind != "" {
			// Stop at the closest harness so a nested worker cannot inherit
			// authority from a primary process further up its ancestry.
			if sameLiveProcess(owner, bootID, process) {
				return nil
			}
			return ErrUnauthorizedCaller
		}
		pid = process.ParentPID
	}
	return ErrUnauthorizedCaller
}
