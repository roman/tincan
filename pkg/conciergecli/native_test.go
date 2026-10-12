package conciergecli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The copied executable provides controlled kernel ancestry for public CLI
// tests. Native Claude/Codex behavior is verified separately in live trials.
func runUnderNativeParent(t *testing.T) bool {
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

func claimNativeParent(t *testing.T, root string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestNativeParentClaim$")
	command.Env = append(os.Environ(), "TINCAN_NATIVE_CLAIM_ROOT="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native claim fixture: %v\n%s", err, output)
	}
}

func TestNativeParentClaim(t *testing.T) {
	root := os.Getenv("TINCAN_NATIVE_CLAIM_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	payload, err := json.Marshal(
		map[string]string{
			"session_id":      fmt.Sprintf("fixture-%d", os.Getppid()),
			"hook_event_name": "SessionStart",
			"cwd":             filepath.Dir(root),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if code := Run(context.Background(), []string{"--root", root, "primary", "claim"}, strings.NewReader(string(payload)), os.Stdout, os.Stderr); code != 0 {
		t.Fatalf("claim code %d", code)
	}
}
