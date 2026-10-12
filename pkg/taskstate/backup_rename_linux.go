package taskstate

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameBackupNoReplace(parent *os.Root, oldName, newName string) error {
	directory, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	fd := int(directory.Fd())
	return unix.Renameat2(fd, oldName, fd, newName, unix.RENAME_NOREPLACE)
}
