package integration_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskLifecycleKeepsReplyAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	packages := []string{}
	for _, name := range []string{
		"concierge-backlog", "concierge-task", "tincan-task-send",
		"tincan-task-reply", "tincan-inbox", "concierge-worker",
	} {
		packages = append(packages, "./cmd/"+name)
	}
	command := exec.Command("go", append([]string{"build", "-buildvcs=false", "-o", bin}, packages...)...)
	command.Dir = ".."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build lifecycle commands: %v: %s", err, output)
	}
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	closed := filepath.Join(root, "pane-closed")
	herdr := `#!/bin/sh
printf '%s\n' "$*" >> "$HERDR_FAKE_LOG"
case "$1 $2" in
  "session list") printf '{"sessions":[{"name":"work","running":true,"socket_path":"/tmp/herdr.sock"}]}' ;;
  "workspace list") printf '{"result":{"workspaces":[{"workspace_id":"ws-1","focused":true,"active_tab_id":"tab-1"}]}}' ;;
  "tab list") printf '{"result":{"tabs":[{"tab_id":"tab-1","focused":true}]}}' ;;
  "pane get")
    if test -f "$HERDR_FAKE_CLOSED"; then
      printf '{"error":{"code":"pane_not_found"}}' >&2
      exit 1
    fi
    printf '{"result":{"pane":{"pane_id":"pane-1","foreground_cwd":"%s","workspace_id":"ws-1","tab_id":"tab-1"}}}' "$HERDR_FAKE_WORKTREE" ;;
  "pane close") touch "$HERDR_FAKE_CLOSED"; printf '{"result":{}}' ;;
  "agent get") printf '{"result":{"agent":{"pane_id":"pane-1","agent_status":"working","agent":"codex"}}}' ;;
  "agent prompt") printf '{"result":{}}' ;;
  "terminal title") printf '{"result":{"reason":"no_foreground_client"}}' ;;
  "pane process-info") printf '{"result":{"type":"pane_process_info","process_info":{"pane_id":"pane-1","shell_pid":100,"foreground_processes":[{"pid":101,"name":"codex","argv0":"codex"}]}}}' ;;
  *) printf 'unexpected Herdr command: %s\n' "$*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(herdr), 0o700); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"HERDR_FAKE_WORKTREE="+worktree,
		"HERDR_FAKE_CLOSED="+closed,
		"HERDR_FAKE_LOG="+filepath.Join(root, "herdr.log"),
	)
	run := func(name string, args ...string) string {
		t.Helper()
		command := exec.Command(filepath.Join(bin, name), args...)
		command.Env = environment
		output, err := command.CombinedOutput()
		if err != nil {
			log, _ := os.ReadFile(filepath.Join(root, "herdr.log"))
			t.Fatalf("%s %v: %v: %s\nHerdr: %s", name, args, err, output, log)
		}
		return string(output)
	}
	field := func(output, name string) string {
		t.Helper()
		for _, line := range strings.Split(output, "\n") {
			if value, found := strings.CutPrefix(line, name+": "); found {
				return value
			}
		}
		t.Fatalf("missing %s in %q", name, output)
		return ""
	}
	messages := filepath.Join(root, "messages")
	run("concierge-backlog", "--root", root, "add", "task-1", "Lifecycle", "--kind", "task", "--repo", "exmachina")
	run("concierge-task", "--root", root, "adopt", "task-1", "--session", "work",
		"--pane", "pane-1", "--name", "worker-1", "--project", root,
		"--worktree", worktree, "--agent-arg", "resume", "--agent-arg", "thread-1")
	run("concierge-backlog", "--root", root, "start", "task-1")
	assignmentFile := filepath.Join(root, "assignment.txt")
	if err := os.WriteFile(assignmentFile, []byte("Inspect the task"), 0o600); err != nil {
		t.Fatal(err)
	}
	assignmentSent := run("tincan-task-send", "--task-root", root, "--message-root", messages,
		"task-1", "--key", "assignment-1", "--body-file", assignmentFile)
	workerMessage := run("tincan-inbox", "--root", messages, "list", "task-1")
	if !strings.Contains(workerMessage, "count: 1") {
		t.Fatalf("worker inbox: %s", workerMessage)
	}
	var assignmentBody string
	read := run("tincan-inbox", "--root", messages, "read", "task-1", field(assignmentSent, "id"))
	if err := json.Unmarshal([]byte(field(read, "body")), &assignmentBody); err != nil {
		t.Fatalf("decode inbox body: %v", err)
	}
	var assignment struct {
		Kind            string `json:"kind"`
		Task            string `json:"task"`
		SpawnGeneration string `json:"spawn_generation"`
		Body            string `json:"body"`
	}
	if err := json.Unmarshal([]byte(assignmentBody), &assignment); err != nil {
		t.Fatalf("decode task assignment: %v", err)
	}
	if assignment.Kind != "task-assignment" || assignment.Task != "task-1" ||
		assignment.Body != "Inspect the task" || assignment.SpawnGeneration == "" {
		t.Fatalf("invalid task assignment: %+v", assignment)
	}
	run("tincan-inbox", "--root", messages, "ack", "task-1", field(assignmentSent, "id"))
	reply := filepath.Join(root, "reply.txt")
	if err := os.WriteFile(reply, []byte("Done"), 0o600); err != nil {
		t.Fatal(err)
	}
	generation := assignment.SpawnGeneration
	if current := field(run("concierge-worker", "--root", root, "show", "task-1"), "spawn_generation"); current != generation {
		t.Fatalf("assignment generation=%q worker generation=%q", generation, current)
	}
	sent := run("tincan-task-reply", "--task-root", root, "--message-root", messages,
		"send", "task-1", "--generation", generation, "--key", "reply-1", "--body-file", reply)
	id := field(sent, "id")
	// The reply sender has exited. A new primary process must still find its message.
	listed := run("tincan-inbox", "--root", messages, "list", "firstmate")
	if !strings.Contains(listed, "count: 1") || !strings.Contains(listed, id) {
		t.Fatalf("reply after exit: %s", listed)
	}
	checked := run("tincan-task-reply", "--task-root", root, "--message-root", messages, "check", id)
	if !strings.Contains(checked, "status: current") {
		t.Fatalf("reply after restart: %s", checked)
	}
	run("concierge-task", "--root", root, "stop", "task-1")
	finished := run("concierge-backlog", "--root", root, "finish", "task-1")
	if !strings.Contains(finished, "state: done") {
		t.Fatalf("finish: %s", finished)
	}
	run("tincan-inbox", "--root", messages, "ack", "firstmate", id)
	if listed := run("tincan-inbox", "--root", messages, "list", "firstmate"); !strings.Contains(listed, "count: 0") {
		t.Fatalf("reply remained pending: %s", listed)
	}
	if listed := run("tincan-inbox", "--root", messages, "list", "task-1"); !strings.Contains(listed, "count: 0") {
		t.Fatalf("assignment remained pending: %s", listed)
	}
}
