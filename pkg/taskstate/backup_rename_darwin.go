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
	return unix.RenameatxNp(fd, oldName, fd, newName, unix.RENAME_EXCL)
}
