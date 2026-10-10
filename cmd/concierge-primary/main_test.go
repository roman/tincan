package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/primary"
)

func TestRunRejectsHookOutsideRootBeforeResolvingProcess(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	input := strings.NewReader(
		`{"session_id":"s1","cwd":"/another/root","hook_event_name":"SessionStart"}`,
	)
	var output bytes.Buffer
	called := false
	status := run(
		[]string{"--root", root, "claim"},
		input,
		&output,
		func(primary.Hook) (primary.Identity, error) {
			called = true
			return primary.Identity{}, fmt.Errorf("unexpected resolver call")
		},
	)
	if status != 2 || called || !strings.Contains(output.String(), "cwd") {
		t.Fatalf("run() = %d, called=%t, output=%q", status, called, output.String())
	}
}

func TestRunRejectsWrongHookEvent(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".concierge")
	input := strings.NewReader(
		fmt.Sprintf(`{"session_id":"s1","cwd":%q,"hook_event_name":"Stop"}`, filepath.Dir(root)),
	)
	var output bytes.Buffer
	status := run(
		[]string{"--root", root, "claim"},
		input,
		&output,
		func(primary.Hook) (primary.Identity, error) {
			return primary.Identity{}, fmt.Errorf("unexpected resolver call")
		},
	)
	if status != 2 || !strings.Contains(output.String(), "SessionStart") {
		t.Fatalf("run() = %d, output=%q", status, output.String())
	}
}
