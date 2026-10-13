package conciergecli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/roman/tincan/internal/nativetest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runUnderNativeParent(t *testing.T) bool {
	t.Helper()
	return nativetest.RunUnderParent(t)
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
