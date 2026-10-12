package conciergecli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/stateversion"
	"github.com/roman/tincan/pkg/taskstate"
)

func taskRoot(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "records"), 0700); err != nil {
		t.Fatal(err)
	}
	guard, err := stateversion.AcquireMigration(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.PublishVersion(2); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func invoke(t *testing.T, root, input string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args = append([]string{"--root", root}, args...)
	if code := Run(context.Background(), args, strings.NewReader(input), &stdout, &stderr); code != 0 ||
		stderr.Len() != 0 {
		t.Fatalf("%v code%d stdout%s stderr%s", args, code, &stdout, &stderr)
	}
	return stdout.String()
}

func TestBacklogIntakeUsesRecordsAndStableRetries(t *testing.T) {
	root := taskRoot(t, t.TempDir())
	args := []string{
		"backlog",
		"add",
		"task-1",
		"Review, \"startup\"",
		"--kind",
		"audit",
		"--repo",
		"tincan",
		"--blocked-by",
		"dependency-1",
		"--blocked-by",
		"dependency-2",
		"--body-file",
		"-",
	}
	want := "task: \"task-1\"\ntitle: \"Review, \\\"startup\\\"\"\nstate: queued\nkind: \"audit\"\nrepo: \"tincan\"\nbacklog: " + quote(
		filepath.Join(root, "backlog.md"),
	) + "\n"
	for range 2 {
		if got := invoke(t, root, "line one\nline two", args...); got != want {
			t.Fatalf("intake=%q want%q", got, want)
		}
	}
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	definition := task.Definition()
	if definition.Body != "line one\nline two" || definition.Created != "" ||
		len(definition.Dependencies) != 2 ||
		definition.Dependencies[0].ID != "dependency-1" ||
		definition.Dependencies[1].ID != "dependency-2" {
		t.Fatalf("definition=%#v", definition)
	}
	if err := os.Remove(filepath.Join(root, "backlog.md")); err != nil {
		t.Fatal(err)
	}
	if got := invoke(t, root, "", "backlog", "list"); got != "count: 1\ntasks[1]{id,state,kind,repo,title}:\n  \"task-1\",queued,\"audit\",\"tincan\",\"Review, \\\"startup\\\"\"\n" {
		t.Fatalf("list=%q", got)
	}
	if got := invoke(t, root, "", "backlog", "show", "task-1", "--full"); !strings.Contains(
		got,
		"body: \"line one\\nline two\"",
	) {
		t.Fatalf("show=%q", got)
	}
	if got := invoke(t, root, "", "backlog", "list", "--state", "done"); got != "count: 0\n" {
		t.Fatalf("done=%q", got)
	}
}

func TestBacklogIntakeReadsBodyFileAndRejectsConflict(t *testing.T) {
	root := taskRoot(t, t.TempDir())
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("é", 250)), 0600); err != nil {
		t.Fatal(err)
	}
	invoke(
		t,
		root,
		"",
		"backlog",
		"add",
		"task-1",
		"Work",
		"--kind",
		"ship",
		"--repo",
		"tincan",
		"--body-file",
		path,
	)
	if got := invoke(t, root, "", "backlog", "show", "task-1"); !strings.Contains(
		got,
		"truncated, 250 chars total",
	) {
		t.Fatalf("summary=%s", got)
	}
	if got := invoke(t, root, "", "backlog", "show", "task-1", "--full"); !strings.Contains(
		got,
		strings.Repeat("é", 250),
	) {
		t.Fatalf("full=%s", got)
	}
	var stdout, stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{
			"--root",
			root,
			"backlog",
			"add",
			"task-1",
			"Changed",
			"--kind",
			"ship",
			"--repo",
			"tincan",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 1 || !strings.Contains(stdout.String(), "error:") ||
		!strings.Contains(stdout.String(), "help:") ||
		stderr.Len() != 0 {
		t.Fatalf("conflict code%d stdout%s stderr%s", code, &stdout, &stderr)
	}
	store, _ := taskstate.NewStore(root)
	task, err := store.Read(context.Background(), "task-1")
	if err != nil || task.Definition().Title != "Work" {
		t.Fatalf("conflict changed record: %v, %v", task, err)
	}
}

func TestConciergeRootPrecedence(t *testing.T) {
	for _, name := range []string{"explicit", "environment", "default"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			t.Chdir(directory)
			t.Setenv("CONCIERGE_HOME", "")
			root := filepath.Join(directory, ".concierge")
			args := []string{"backlog", "list"}
			switch name {
			case "explicit":
				root = filepath.Join(directory, "explicit")
				t.Setenv("CONCIERGE_HOME", filepath.Join(directory, "unused"))
				args = append([]string{"--root", root}, args...)
			case "environment":
				root = filepath.Join(directory, "environment")
				t.Setenv("CONCIERGE_HOME", root)
			}
			taskRoot(t, root)
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 ||
				stdout.String() != "count: 0\n" {
				t.Fatalf("root code%d stdout%s stderr%s", code, &stdout, &stderr)
			}
			if _, err := os.Stat(filepath.Join(directory, "unused")); !os.IsNotExist(err) {
				t.Fatalf("unused root changed: %v", err)
			}
		})
	}
}

func TestConciergeHelpAndVersionAvoidDiscovery(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	t.Setenv("CONCIERGE_HOME", "")
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--help"}, {"backlog", "add", "--help"}, {"--version"}, {"-v"}, {"-V"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != 0 ||
			stdout.Len() == 0 ||
			stderr.Len() != 0 {
			t.Fatalf("%v code%d stdout%s stderr%s", args, code, &stdout, &stderr)
		}
		if len(args) == 1 && args[0] != "--help" && stdout.String() != "0.1.0\n" {
			t.Fatalf("version=%q", stdout.String())
		}
	}
}

func TestBacklogUsageAndSetupErrors(t *testing.T) {
	for _, trial := range []struct {
		name string
		args []string
		code int
	}{
		{"unknown flag", []string{"backlog", "list", "--typo"}, 2}, {"bad state", []string{"backlog", "list", "--state", "invalid"}, 2}, {"missing kind", []string{"backlog", "add", "task-1", "Work", "--repo", "tincan"}, 2}, {"relative root", []string{"--root", "relative", "backlog", "list"}, 2}, {"uninitialized", []string{"backlog", "list"}, 1},
	} {
		t.Run(trial.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent")
			args := append([]string{"--root", root}, trial.args...)
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); code != trial.code ||
				!strings.Contains(stdout.String(), "error:") ||
				!strings.Contains(stdout.String(), "help:") ||
				stderr.Len() != 0 {
				t.Fatalf("code%d stdout%s stderr%s", code, &stdout, &stderr)
			}
			if _, err := os.Stat(filepath.Join(root, "records")); !os.IsNotExist(err) {
				t.Fatalf("initialized task storage: %v", err)
			}
		})
	}
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--root", root, "backlog", "list"}, strings.NewReader(""), &stdout, &stderr); code != 1 ||
		!strings.Contains(stdout.String(), "error:") {
		t.Fatalf("v1 root code%d stdout%s", code, &stdout)
	}
}
