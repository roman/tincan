package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareLeasedUsesExactBaseAndRecoversItsBranch(t *testing.T) {
	client, specification := checkoutForTest(t)
	if err := os.WriteFile(filepath.Join(specification.Project, "file.txt"), []byte("later main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitForTest(t, specification.Project, "add", "file.txt")
	gitForTest(
		t,
		specification.Project,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@example.invalid",
		"commit",
		"-m",
		"Advance main",
	)
	gitForTest(t, specification.Project, "worktree", "add", "--detach", specification.Path, "main")
	for range 2 {
		if err := client.PrepareLeased(context.Background(), specification); err != nil {
			t.Fatalf("prepare exact-base branch in lease: %v", err)
		}
	}
	if got := gitForTest(t, specification.Path, "rev-parse", "HEAD"); got != specification.BaseCommit {
		t.Fatalf("leased HEAD=%s, want %s", got, specification.BaseCommit)
	}
	if got := gitForTest(t, specification.Path, "symbolic-ref", "HEAD"); got != "refs/heads/"+specification.Branch {
		t.Fatalf("leased branch=%s", got)
	}
}

func TestPrepareLeasedRejectsChangedFilesAndBranchConflicts(t *testing.T) {
	for _, condition := range []string{"tracked changes", "untracked file", "existing branch", "different branch", "hidden changes", "locked registration"} {
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
			case "tracked changes":
				if err := os.WriteFile(filepath.Join(specification.Path, "file.txt"), []byte("keep this work\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "untracked file":
				if err := os.WriteFile(filepath.Join(specification.Path, "pending.txt"), []byte("keep this work\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "existing branch":
				gitForTest(
					t,
					specification.Project,
					"branch",
					specification.Branch,
					specification.BaseCommit,
				)
			case "different branch":
				gitForTest(t, specification.Path, "switch", "-c", "other/work")
			case "hidden changes":
				gitForTest(t, specification.Path, "update-index", "--assume-unchanged", "file.txt")
				if err := os.WriteFile(filepath.Join(specification.Path, "file.txt"), []byte("hidden work\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "locked registration":
				gitForTest(t, specification.Project, "worktree", "lock", specification.Path)
			}
			beforeHead := gitForTest(t, specification.Path, "rev-parse", "HEAD")
			beforeStatus := gitForTest(
				t,
				specification.Path,
				"status",
				"--porcelain=v1",
				"--untracked-files=all",
			)
			before, _ := os.ReadFile(filepath.Join(specification.Path, "file.txt"))
			if err := client.PrepareLeased(context.Background(), specification); err == nil {
				t.Fatal("conflicting lease accepted")
			}
			if got := gitForTest(t, specification.Path, "rev-parse", "HEAD"); got != beforeHead {
				t.Fatal("rejected lease changed HEAD")
			}
			if got := gitForTest(t, specification.Path, "status", "--porcelain=v1", "--untracked-files=all"); got != beforeStatus {
				t.Fatal("rejected lease changed files")
			}
			after, _ := os.ReadFile(filepath.Join(specification.Path, "file.txt"))
			if string(after) != string(before) {
				t.Fatal("rejected lease discarded hidden work")
			}
		})
	}
}

func TestPrepareLeasedPreservesIgnoredFilesThatConflictWithExactBase(t *testing.T) {
	client, specification := checkoutForTest(t)
	gitForTest(t, specification.Project, "rm", "file.txt")
	if err := os.WriteFile(filepath.Join(specification.Project, ".gitignore"), []byte("file.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitForTest(t, specification.Project, "add", ".gitignore")
	gitForTest(
		t,
		specification.Project,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@example.invalid",
		"commit",
		"-m",
		"Ignore a removed file",
	)
	gitForTest(t, specification.Project, "worktree", "add", "--detach", specification.Path, "main")
	path := filepath.Join(specification.Path, "file.txt")
	if err := os.WriteFile(path, []byte("valuable ignored work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := gitForTest(t, specification.Path, "rev-parse", "HEAD")
	if err := client.PrepareLeased(context.Background(), specification); err == nil {
		t.Fatal("ignored-file conflict accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "valuable ignored work\n" {
		t.Fatalf("discarded ignored work: %q, %v", data, err)
	}
	if after := gitForTest(t, specification.Path, "rev-parse", "HEAD"); after != before {
		t.Fatal("conflict changed HEAD")
	}
}

func TestPrepareLeasedPreservesCommitsOnTheReservedBranch(t *testing.T) {
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
	if err := client.PrepareLeased(context.Background(), specification); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(specification.Path, "file.txt")
	if err := os.WriteFile(path, []byte("committed result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitForTest(t, specification.Path, "add", "file.txt")
	gitForTest(
		t,
		specification.Path,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@example.invalid",
		"commit",
		"-m",
		"Preserve result",
	)
	result := gitForTest(t, specification.Path, "rev-parse", "HEAD")
	if err := client.PrepareLeased(context.Background(), specification); err == nil {
		t.Fatal("advanced task branch reset to its old base")
	}
	if head := gitForTest(t, specification.Path, "rev-parse", "HEAD"); head != result {
		t.Fatal("retry changed result commit")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "committed result\n" {
		t.Fatalf("retry changed result: %q, %v", data, err)
	}
}

func TestPrepareLeasedRejectsSourceCheckoutThroughSubdirectory(t *testing.T) {
	for _, suffix := range []string{"", " "} {
		t.Run("suffix="+suffix, func(t *testing.T) {
			client, specification := checkoutForTest(t)
			source := specification.Project + suffix
			if suffix != "" {
				if err := os.Rename(specification.Project, source); err != nil {
					t.Fatal(err)
				}
			}
			subdirectory := filepath.Join(source, "nested")
			if err := os.Mkdir(subdirectory, 0700); err != nil {
				t.Fatal(err)
			}
			specification.Project = subdirectory
			specification.Path = source
			specification.Branch = "main"
			if err := client.PrepareLeased(context.Background(), specification); err == nil {
				t.Fatal("source checkout accepted through a project subdirectory")
			}
			if head := gitForTest(t, source, "rev-parse", "HEAD"); head != specification.BaseCommit {
				t.Fatal("source checkout changed")
			}
		})
	}
}
