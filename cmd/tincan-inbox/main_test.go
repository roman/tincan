package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSendRequiresExplicitBodySource(t *testing.T) {
	root := t.TempDir()
	command := &CLI{Root: root}
	command.Send.Recipient = "worker"
	if err := run(command, "send <recipient>"); err == nil {
		t.Fatal("send without a body source succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, "inbox")); !os.IsNotExist(err) {
		t.Fatalf("inbox was created: %v", err)
	}
}
