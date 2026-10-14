package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnusedLeaseReturnPreservesUnsafeDetachedWork(t *testing.T) {
	for _, condition := range []string{"clean reachable", "orphan commit", "dirty file", "ignored file", "different branch"} {
		t.Run(condition, func(t *testing.T) {
			client, specification := checkoutForTest(t)
			gitForTest(
				t,
				specification.Project,
				"worktree",
				"add",
				"--detach",
				specification.Path,
				specification.BaseCommit,
			)
			switch condition {
			case "orphan commit":
				gitForTest(t, specification.Path, "switch", "--orphan", "orphan-result")
				gitForTest(
					t,
					specification.Path,
					"-c",
					"user.name=Test",
					"-c",
					"user.email=test@example.invalid",
					"commit",
					"--allow-empty",
					"-m",
					"Detached work",
				)
				gitForTest(t, specification.Path, "switch", "--detach")
				gitForTest(t, specification.Project, "branch", "-D", "orphan-result")
			case "dirty file":
				if err := os.WriteFile(filepath.Join(specification.Path, "file.txt"), []byte("pending work\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ignored file":
				if err := os.WriteFile(filepath.Join(specification.Project, ".git", "info", "exclude"), []byte("private.txt\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(specification.Path, "private.txt"), []byte("pending ignored work\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "different branch":
				gitForTest(t, specification.Path, "switch", "-c", "other-work")
			}
			before := gitForTest(t, specification.Path, "rev-parse", "HEAD")
			err := client.VerifyUnusedLeaseReturn(context.Background(), specification)
			if (err == nil) != (condition == "clean reachable") {
				t.Fatalf("return verification: %v", err)
			}
			if after := gitForTest(t, specification.Path, "rev-parse", "HEAD"); after != before {
				t.Fatal("verification changed HEAD")
			}
		})
	}
}
