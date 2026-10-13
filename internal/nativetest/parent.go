// Package nativetest supplies controlled process ancestry for admission tests.
package nativetest

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// The copied executable provides controlled kernel ancestry for public CLI
// tests. Native Claude/Codex behavior is verified separately in live trials.
func RunUnderParent(t *testing.T) bool {
	t.Helper()
	if os.Getenv("TINCAN_NATIVE_CALLER_TEST") == t.Name() {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	path := filepath.Join(t.TempDir(), "codex")
	destination, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(path, "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
	command.Env = append(os.Environ(), "TINCAN_NATIVE_CALLER_TEST="+t.Name())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native caller fixture: %v\n%s", err, output)
	}
	return true
}
