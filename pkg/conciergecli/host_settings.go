package conciergecli

import (
	"os/exec"
	"path/filepath"
)

func resolveHerdrExecutable(value string) (string, error) {
	executable, err := exec.LookPath(value)
	if err != nil {
		return "", err
	}
	return filepath.Abs(executable)
}
