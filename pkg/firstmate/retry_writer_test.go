package firstmate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRetryWriterAllowsOneProcess(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := AcquireRetryWriter(state)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcquireRetryWriter(state)
	if !errors.Is(err, ErrRetryWriterBusy) {
		t.Fatalf("second writer error=%v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireRetryWriter(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryWriterReleasesAfterProcessExit(t *testing.T) {
	state := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=TestRetryWriterChild")
	command.Env = append(os.Environ(), "TINCAN_TEST_RETRY_STATE="+state)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("child error=%v output=%s", err, output)
	}
	path, err := ParseAbsolutePath(state)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := AcquireRetryWriter(path)
	if err != nil {
		t.Fatalf("writer after process exit: %v", err)
	}
	if err := writer.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryWriterChild(t *testing.T) {
	state := os.Getenv("TINCAN_TEST_RETRY_STATE")
	if state == "" {
		return
	}
	path, err := ParseAbsolutePath(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireRetryWriter(path); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestRetryWriterUsesSameLockThroughSymlink(t *testing.T) {
	state, err := ParseAbsolutePath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "state-alias")
	if err := os.Symlink(state.String(), alias); err != nil {
		t.Fatal(err)
	}
	aliasPath, err := ParseAbsolutePath(alias)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := AcquireRetryWriter(state)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Release()
	if writer.StateDir.String() != state.String() {
		t.Fatalf("canonical state=%q", writer.StateDir)
	}
	if _, err := AcquireRetryWriter(aliasPath); !errors.Is(err, ErrRetryWriterBusy) {
		t.Fatalf("alias writer error=%v", err)
	}
	other := t.TempDir()
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	if writer.StateDir.String() != state.String() {
		t.Fatalf("retargeted state=%q", writer.StateDir)
	}
}
