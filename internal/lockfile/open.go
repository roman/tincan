// Package lockfile opens persistent advisory-lock files within pinned roots.
package lockfile

import (
	"errors"
	"os"
)

func Open(root *os.Root, name string) (*os.File, error) {
	// Concurrent O_CREATE without O_EXCL can return ENOENT on Darwin.
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		return root.OpenFile(name, os.O_RDWR, 0)
	}
	return file, err
}
