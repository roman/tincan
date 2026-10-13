package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitForTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func checkoutForTest(t *testing.T) (Client, Specification) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	gitForTest(t, project, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(project, "file.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitForTest(t, project, "add", "file.txt")
	gitForTest(
		t,
		project,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@example.invalid",
		"commit",
		"-m",
		"Initial",
	)
	executable, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return Client{Executable: executable}, Specification{
		Project: project, Path: filepath.Join(root, "worker"), Branch: "task/work",
		BaseCommit: gitForTest(t, project, "rev-parse", "HEAD"),
	}
}

func TestPrepareRecoversCreatedWorktreeWithoutReplacingIt(t *testing.T) {
	client, specification := checkoutForTest(t)
	for range 2 {
		if err := client.Prepare(context.Background(), specification); err != nil {
			t.Fatal(err)
		}
	}
	if got := gitForTest(t, specification.Path, "rev-parse", "HEAD"); got != specification.BaseCommit {
		t.Fatalf("HEAD = %s", got)
	}
	if got := gitForTest(t, specification.Path, "symbolic-ref", "HEAD"); got != "refs/heads/"+specification.Branch {
		t.Fatalf("branch = %s", got)
	}
}

func TestValidateRejectsUnusableBaseWithoutCreatingWorktree(t *testing.T) {
	for _, invalid := range []string{"branch", "missing commit", "blob"} {
		t.Run(invalid, func(t *testing.T) {
			client, specification := checkoutForTest(t)
			switch invalid {
			case "branch":
				specification.Branch = "bad..branch"
			case "missing commit":
				specification.BaseCommit = strings.Repeat("a", 40)
			case "blob":
				specification.BaseCommit = gitForTest(
					t,
					specification.Project,
					"rev-parse",
					"HEAD:file.txt",
				)
			}
			if err := client.Validate(context.Background(), specification); err == nil {
				t.Fatal("unusable specification accepted")
			}
			if _, err := os.Stat(specification.Path); !os.IsNotExist(err) {
				t.Fatal("validation created a worktree")
			}
		})
	}
}

func TestPrepareRejectsIncompleteOrChangedCheckout(t *testing.T) {
	for _, state := range []string{"incomplete", "modified", "index locked", "registration locked", "sparse", "assume unchanged"} {
		t.Run(state, func(t *testing.T) {
			client, specification := checkoutForTest(t)
			arguments := []string{"worktree", "add", "-b", specification.Branch}
			if state == "incomplete" {
				arguments = append(arguments, "--no-checkout")
			}
			gitForTest(
				t,
				specification.Project,
				append(arguments, specification.Path, specification.BaseCommit)...)
			switch state {
			case "modified":
				if err := os.WriteFile(filepath.Join(specification.Path, "file.txt"), []byte("changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "index locked":
				lock := gitForTest(
					t,
					specification.Path,
					"rev-parse",
					"--path-format=absolute",
					"--git-path",
					"index.lock",
				)
				if err := os.WriteFile(lock, []byte("held"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "registration locked":
				gitForTest(t, specification.Project, "worktree", "lock", specification.Path)
			case "sparse":
				gitForTest(
					t,
					specification.Path,
					"sparse-checkout",
					"set",
					"--no-cone",
					"absent.txt",
				)
				if _, err := os.Stat(filepath.Join(specification.Path, "file.txt")); !os.IsNotExist(
					err,
				) {
					t.Fatalf("sparse checkout retained excluded file: %v", err)
				}
			case "assume unchanged":
				gitForTest(t, specification.Path, "update-index", "--assume-unchanged", "file.txt")
				if err := os.WriteFile(filepath.Join(specification.Path, "file.txt"), []byte("hidden change\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if state == "sparse" || state == "assume unchanged" {
				if status := gitForTest(t, specification.Path, "status", "--porcelain", "--untracked-files=no"); status != "" {
					t.Fatalf("fixture must hide tracked differences from status: %s", status)
				}
			}
			if err := client.Prepare(context.Background(), specification); err == nil {
				t.Fatal("unusable initial checkout accepted")
			}
		})
	}
}

func TestPreparePreservesUnregisteredDirectoryAndExistingBranch(t *testing.T) {
	for _, conflict := range []string{"directory", "branch through alias", "literal branch"} {
		t.Run(conflict, func(t *testing.T) {
			client, specification := checkoutForTest(t)
			switch conflict {
			case "directory":
				if err := os.Mkdir(specification.Path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(specification.Path, "keep"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "branch through alias":
				gitForTest(t, specification.Project, "branch", specification.Branch)
				alias := specification.Project + "-alias"
				if err := os.Symlink(specification.Project, alias); err != nil {
					t.Fatal(err)
				}
				specification.Project = alias
			case "literal branch":
				specification.Branch = "@{-1}"
			}
			if err := client.Prepare(context.Background(), specification); err == nil {
				t.Fatal("conflicting preparation accepted")
			}
			if conflict == "directory" {
				if data, err := os.ReadFile(filepath.Join(specification.Path, "keep")); err != nil ||
					string(data) != "keep" {
					t.Fatal("existing directory changed")
				}
			} else if _, err := os.Stat(specification.Path); !os.IsNotExist(err) {
				t.Fatalf("conflict created worktree: %v", err)
			}
		})
	}
}

func TestIdentityVerificationPreservesWorkerChanges(t *testing.T) {
	client, specification := checkoutForTest(t)
	if err := client.Prepare(context.Background(), specification); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specification.Path, "file.txt"), []byte("worker commit\n"), 0o600); err != nil {
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
		"Worker result",
	)
	workerCommit := gitForTest(t, specification.Path, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(specification.Path, "file.txt"), []byte("unfinished worker change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyIdentity(context.Background(), specification); err != nil {
		t.Fatal(err)
	}
	if err := client.Prepare(context.Background(), specification); err == nil {
		t.Fatal("initial preparation accepted later worker state")
	}
	if got := gitForTest(t, specification.Path, "rev-parse", "HEAD"); got != workerCommit {
		t.Fatalf("worker commit changed: %s", got)
	}
	if data, err := os.ReadFile(filepath.Join(specification.Path, "file.txt")); err != nil ||
		string(data) != "unfinished worker change\n" {
		t.Fatal("worker changes lost")
	}
}
