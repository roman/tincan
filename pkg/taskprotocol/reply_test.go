package taskprotocol

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestDecodeReplyChecksStateAndBoundKey(t *testing.T) {
	root := t.TempDir()
	body, err := json.Marshal(Reply{
		Kind: "task-reply", State: filepath.Join(root, "tasks"),
		Task: "task-1", SpawnGeneration: "gen-1", AssignmentID: "k-" +
			"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ReplyKey: "answer-1", Body: "Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "task-reply:" + root + ":task-1:gen-1:k-" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if _, err := DecodeReply(string(body), key, root); err != nil {
		t.Fatalf("valid bound reply: %v", err)
	}
	for _, test := range []struct {
		name string
		key  string
		root string
	}{
		{"changed key", "other", root},
		{"changed root", key, t.TempDir()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeReply(string(body), test.key, test.root); !errors.Is(err, ErrInvalidReply) {
				t.Fatalf("accepted invalid reply: %v", err)
			}
		})
	}
}
