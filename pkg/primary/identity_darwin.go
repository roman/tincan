package primary

import (
	"errors"
	"golang.org/x/sys/unix"
	"strings"
)

func readBootID() (string, error) {
	value, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("boot-session UUID is unavailable")
	}
	return value, nil
}

func readPIDNamespace() (string, error) {
	// Darwin has one host process namespace. The boot UUID scopes process identity.
	return "darwin", nil
}
