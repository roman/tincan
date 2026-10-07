package inbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

// rootPath is an absolute, validated inbox root.
type rootPath struct{ value string }

// mailboxPath is one recipient's directory beneath a root.
type mailboxPath struct{ value string }

// messageDirectoryPath is a pending or handled message directory.
type messageDirectoryPath struct{ value string }

// filePath is a lock or message file beneath a mailbox.
type filePath struct{ value string }

func newRootPath(raw string) (rootPath, error) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return rootPath{}, ErrInvalidRoot
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return rootPath{}, fmt.Errorf("resolve inbox root: %w", err)
	}
	return rootPath{value: absolute}, nil
}
func (path rootPath) String() string             { return path.value }
func (path mailboxPath) String() string          { return path.value }
func (path messageDirectoryPath) String() string { return path.value }
func (path filePath) String() string             { return path.value }
func (path rootPath) mailboxPath(recipient Recipient) mailboxPath {
	return mailboxPath{value: filepath.Join(path.value, "inbox", recipient.String())}
}
func (path mailboxPath) pendingPath() messageDirectoryPath {
	return messageDirectoryPath{value: filepath.Join(path.value, "pending")}
}
func (path mailboxPath) handledPath() messageDirectoryPath {
	return messageDirectoryPath{value: filepath.Join(path.value, "handled")}
}
func (path mailboxPath) lockPath() filePath {
	return filePath{value: filepath.Join(path.value, ".lock")}
}
func (path messageDirectoryPath) messagePath(id MessageID) filePath {
	return filePath{value: filepath.Join(path.value, id.String()+".json")}
}
