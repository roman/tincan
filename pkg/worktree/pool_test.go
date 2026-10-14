package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPoolAcquireConfirmsDurableLeaseIdentity(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "treehouse")
	script := `#!/bin/sh
case "$1" in
get)
 [ "$2" = --lease ] && [ "$3" = --json ] && [ "$4" = --no-fetch ] && [ "$5" = --lease-holder ] && [ "$6" = task-holder ] || exit 72
 printf '{"path":"%s/worker","lease_id":"lease-1","lease_holder":"task-holder"}\n' "$POOL_TEST_ROOT"
 ;;
status)
 [ "$2" = --json ] || exit 73
 printf '[{"path":"%s/worker","lease_id":"lease-1","lease_holder":"task-holder","status":"leased","flavor":"git","processes":[]}]\n' "$POOL_TEST_ROOT"
 ;;
*) exit 74 ;;
esac
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_ROOT", root)
	lease, err := (PoolClient{Executable: executable}).Acquire(
		context.Background(),
		root,
		"task-holder",
	)
	if err != nil || lease.ID != "lease-1" || lease.Holder != "task-holder" ||
		lease.Path != filepath.Join(root, "worker") {
		t.Fatalf("confirmed lease: %+v, %v", lease, err)
	}
}

func TestPoolReturnGuardsIdentityAndConfirmsRelease(t *testing.T) {
	for _, test := range []struct {
		name, id, holder, processes string
		staysLeased, wantError      bool
		calls                       int
	}{
		{"released", "lease-1", "task-holder", "[]", false, false, 1},
		{"rebound", "lease-2", "other-holder", "[]", false, true, 0},
		{"live process", "lease-1", "task-holder", `[{"pid":123}]`, false, true, 0},
		{"false success", "lease-1", "task-holder", "[]", true, true, 1},
		{"already returned", "", "", "[]", false, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executable := filepath.Join(root, "treehouse")
			inventory, post, record := filepath.Join(
				root,
				"inventory",
			), filepath.Join(
				root,
				"post",
			), filepath.Join(
				root,
				"calls",
			)
			path := filepath.Join(root, "worker")
			before := fmt.Sprintf(
				`[{"path":%q,"lease_id":%q,"lease_holder":%q,"flavor":"git","processes":%s}]`,
				path,
				test.id,
				test.holder,
				test.processes,
			)
			after := fmt.Sprintf(
				`[{"path":%q,"lease_id":"","lease_holder":"","flavor":"git","processes":[]}]`,
				path,
			)
			if test.staysLeased {
				after = before
			}
			if err := os.WriteFile(inventory, []byte(before), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(post, []byte(after), 0600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
case "$1" in
status) cat "$POOL_INVENTORY" ;;
return)
 [ "$2" = "$POOL_PATH" ] && [ "$3" = --if-lease-id ] && [ "$4" = lease-1 ] && [ "$5" = --if-lease-holder ] && [ "$6" = task-holder ] && [ "$#" = 6 ] || exit 72
 if read -r approval; then exit 73; fi
 printf 'return\n' >> "$POOL_RECORD"
 cp "$POOL_POST" "$POOL_INVENTORY"
 ;;
*) exit 74 ;;
esac
`
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			for name, value := range map[string]string{"POOL_INVENTORY": inventory, "POOL_POST": post, "POOL_RECORD": record, "POOL_PATH": path} {
				t.Setenv(name, value)
			}
			client := PoolClient{Executable: executable}
			lease := Lease{Path: path, ID: "lease-1", Holder: "task-holder"}
			err := client.Return(context.Background(), root, lease)
			if (err != nil) != test.wantError {
				t.Fatalf("return: %v", err)
			}
			if !test.wantError {
				if err := client.Return(context.Background(), root, lease); err != nil {
					t.Fatalf("confirmed return retry: %v", err)
				}
			}
			data, _ := os.ReadFile(record)
			if strings.Count(string(data), "return\n") != test.calls {
				t.Fatalf("return calls=%q, want %d", data, test.calls)
			}
		})
	}
}

func TestPoolFindRecoversAnAcquisitionAfterLostResponse(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "treehouse")
	record := filepath.Join(root, "calls")
	script := `#!/bin/sh
case "$1" in
get) printf 'get\n' >> "$POOL_RECORD"; exit 72 ;;
status) printf '[{"path":"%s/worker","lease_id":"lease-1","lease_holder":"task-holder","flavor":"git","processes":[]}]\n' "$POOL_TEST_ROOT" ;;
*) exit 73 ;;
esac
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_ROOT", root)
	t.Setenv("POOL_RECORD", record)
	client := PoolClient{Executable: executable}
	if _, err := client.Acquire(context.Background(), root, "task-holder"); err == nil {
		t.Fatal("lost response succeeded")
	}
	lease, found, err := client.Find(context.Background(), root, "task-holder")
	if err != nil || !found || lease.ID != "lease-1" {
		t.Fatalf("recover published lease: %+v, %t, %v", lease, found, err)
	}
	data, _ := os.ReadFile(record)
	if string(data) != "get\n" {
		t.Fatalf("recovery repeated acquisition: %q", data)
	}
}

func TestPoolIntegrationPreservesDirtyLeaseAndCommittedBranch(t *testing.T) {
	executable := os.Getenv("TREEHOUSE_TEST_EXECUTABLE")
	if executable == "" {
		t.Skip("set TREEHOUSE_TEST_EXECUTABLE to the pinned treehouse binary")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(directory string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", directory}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git(root, "init", "-b", "main")
	git(
		root,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@example.invalid",
		"commit",
		"--allow-empty",
		"-m",
		"Initial",
	)
	if err := os.WriteFile(filepath.Join(root, "treehouse.toml"), []byte("max_trees = 2\nroot = \"./\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := PoolClient{Executable: executable}
	lease, err := client.Acquire(context.Background(), root, "task-holder")
	if err != nil {
		t.Fatal(err)
	}
	git(lease.Path, "switch", "-c", "task/result")
	resultFile := filepath.Join(lease.Path, "result.txt")
	if err := os.WriteFile(resultFile, []byte("unfinished result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.Return(context.Background(), root, lease); err == nil {
		t.Fatal("dirty lease returned without approval")
	}
	kept, found, err := client.Find(context.Background(), root, lease.Holder)
	if err != nil || !found || kept != lease {
		t.Fatalf("dirty result lost lease: %+v, %t, %v", kept, found, err)
	}
	git(lease.Path, "add", "result.txt")
	git(
		lease.Path,
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@example.invalid",
		"commit",
		"-m",
		"Preserve result",
	)
	resultCommit := git(lease.Path, "rev-parse", "HEAD")
	if err := client.Return(context.Background(), root, lease); err != nil {
		t.Fatal(err)
	}
	if _, found, err := client.Find(context.Background(), root, lease.Holder); err != nil || found {
		t.Fatalf("released lease remains: %t, %v", found, err)
	}
	if got := git(root, "rev-parse", "refs/heads/task/result"); got != resultCommit {
		t.Fatalf("return discarded result branch: %s", got)
	}
	if err := client.Return(context.Background(), root, lease); err != nil {
		t.Fatalf("real return retry: %v", err)
	}
}

func TestPoolNeverRecoversOrReturnsTheSourceCheckout(t *testing.T) {
	for _, operation := range []string{"acquire", "find", "return"} {
		for _, alias := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/alias=%t", operation, alias), func(t *testing.T) {
				root := t.TempDir()
				path := root
				if alias {
					path = filepath.Join(root, "alias")
					if err := os.Symlink(root, path); err != nil {
						t.Fatal(err)
					}
				}
				executable, record := filepath.Join(
					root,
					"treehouse",
				), filepath.Join(
					root,
					"returns",
				)
				script := `#!/bin/sh
case "$1" in
get) printf '{"path":"%s","lease_id":"lease-1","lease_holder":"task-holder"}\n' "$POOL_SOURCE_PATH" ;;
status) printf '[{"path":"%s","lease_id":"lease-1","lease_holder":"task-holder","flavor":"git","processes":[]}]\n' "$POOL_SOURCE_PATH" ;;
return) printf returned > "$POOL_RETURN_RECORD" ;;
esac
`
				if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("POOL_SOURCE_PATH", path)
				t.Setenv("POOL_RETURN_RECORD", record)
				client := PoolClient{Executable: executable}
				var err error
				switch operation {
				case "acquire":
					_, err = client.Acquire(context.Background(), root, "task-holder")
				case "find":
					_, _, err = client.Find(context.Background(), root, "task-holder")
				case "return":
					err = client.Return(
						context.Background(),
						root,
						Lease{Path: path, ID: "lease-1", Holder: "task-holder"},
					)
				}
				if err == nil {
					t.Fatal("accepted the source checkout as a pooled worker")
				}
				if _, err := os.Stat(record); !os.IsNotExist(err) {
					t.Fatal("return targeted the source checkout")
				}
			})
		}
	}
}

func TestPoolFindRejectsIncompleteOrAmbiguousInventory(t *testing.T) {
	for _, test := range []struct{ name, inventory string }{
		{"null", "null"},
		{"missing process inventory", `[{"path":"/worker","lease_id":"lease-1","lease_holder":"task-holder","flavor":"git"}]`},
		{"duplicate holder", `[{"path":"/worker-1","lease_id":"lease-1","lease_holder":"task-holder","flavor":"git","processes":[]},{"path":"/worker-2","lease_id":"lease-2","lease_holder":"task-holder","flavor":"git","processes":[]}]`},
		{"damaged lease", `[{"path":"/worker","lease_id":"lease-1","lease_holder":"task-holder","flavor":"git","recovery_reason":"unknown reservation","processes":[]}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executable := filepath.Join(root, "treehouse")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$POOL_LIST\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("POOL_LIST", test.inventory)
			if _, _, err := (PoolClient{Executable: executable}).Find(context.Background(), root, "task-holder"); err == nil {
				t.Fatal("accepted ambiguous or incomplete lease inventory")
			}
		})
	}
}
