package taskops

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/internal/herdr"
	"github.com/roman/tincan/pkg/taskstate"
)

func TestNativeLaunchArgumentsPreserveResumeAndReplaceGeneration(t *testing.T) {
	for _, test := range []struct {
		harness      string
		args, prefix []string
	}{
		{"codex", []string{"resume", "thread-1", "--model", "model-1", "-c", "features.hooks=false"}, []string{"-c", `shell_environment_policy.set.CONCIERGE_SPAWN_GENERATION="gen-new"`}},
		{"claude", []string{"--resume", "thread-1", "--model", "model-1"}, []string{"--settings", `{"env":{"CONCIERGE_SPAWN_GENERATION":"gen-new"}}`}},
	} {
		t.Run(test.harness, func(t *testing.T) {
			original := append([]string{}, test.args...)
			for _, generation := range []string{"gen-old", "gen-new", "gen-new"} {
				got, err := nativeLaunchArguments(
					test.harness,
					test.args,
					generation,
					"static protocol",
				)
				if err != nil {
					t.Fatal(err)
				}
				prefix := []string{
					test.prefix[0],
					strings.ReplaceAll(test.prefix[1], "gen-new", generation),
				}
				if !reflect.DeepEqual(got, append(append(prefix, original...), "static protocol")) {
					t.Fatalf("launch arguments: %v", got)
				}
				if !reflect.DeepEqual(test.args, original) {
					t.Fatalf("mutated reusable arguments: %v", test.args)
				}
			}
		})
	}
}

func TestNativeResumeArgumentsRequireMatchingIDReference(t *testing.T) {
	for _, harness := range []string{"codex", "claude"} {
		t.Run(harness, func(t *testing.T) {
			reference := herdr.SessionReference{
				Source: "herdr:" + harness,
				Agent:  harness,
				Kind:   "id",
				Value:  "actual-native-id",
			}
			arguments, err := nativeResumeArguments(harness, &reference)
			flag := "resume"
			if harness == "claude" {
				flag = "--resume"
			}
			if err != nil || !reflect.DeepEqual(arguments, []string{flag, reference.Value}) {
				t.Fatalf("resume arguments = %v, %v", arguments, err)
			}
			for _, test := range []struct {
				name   string
				change func(*herdr.SessionReference)
			}{
				{"wrong source", func(r *herdr.SessionReference) { r.Source = "other" }},
				{"wrong harness", func(r *herdr.SessionReference) { r.Agent = "other" }},
				{"path reference", func(r *herdr.SessionReference) { r.Kind = "path" }},
				{"empty ID", func(r *herdr.SessionReference) { r.Value = "" }},
				{"blank ID", func(r *herdr.SessionReference) { r.Value = "  " }},
				{"option ID", func(r *herdr.SessionReference) { r.Value = "--last" }},
				{"control ID", func(r *herdr.SessionReference) { r.Value = "id\nother" }},
				{"oversized ID", func(r *herdr.SessionReference) { r.Value = strings.Repeat("a", 513) }},
			} {
				t.Run(test.name, func(t *testing.T) {
					invalid := reference
					test.change(&invalid)
					if _, err := nativeResumeArguments(harness, &invalid); err == nil {
						t.Fatal("unsafe resume identity accepted")
					}
				})
			}
			if _, err := nativeResumeArguments(harness, nil); err == nil {
				t.Fatal("missing native identity accepted")
			}
		})
	}
	if _, err := nativeResumeArguments("other", &herdr.SessionReference{Source: "herdr:other", Agent: "other", Kind: "id", Value: "native-id"}); err == nil {
		t.Fatal("unsupported harness accepted")
	}
}

func TestNativeLaunchRejectsConflictingSettings(t *testing.T) {
	for _, test := range []struct {
		harness string
		args    []string
	}{
		{"codex", []string{"-c", "shell_environment_policy={inherit=\"all\"}"}},
		{"codex", []string{"--config=shell_environment_policy.set.CONCIERGE_SPAWN_GENERATION=\"old\""}},
		{"codex", []string{"-c=shell_environment_policy.include_only=[]"}},
		{"codex", []string{"-cshell_environment_policy.inherit=\"none\""}},
		{"codex", []string{"--config", "'shell_environment_policy'.set={}"}},
		{"codex", []string{"-c"}},
		{"claude", []string{"--settings", "settings.json"}},
		{"claude", []string{"--settings={}"}},
		{"other", []string{"resume", "thread-1"}},
	} {
		t.Run(test.harness+"/"+test.args[0], func(t *testing.T) {
			if _, err := nativeLaunchArguments(test.harness, test.args, "gen-new", "static protocol"); err == nil {
				t.Fatal("conflicting launch accepted")
			}
		})
	}
	for _, generation := range []string{"", "bad generation"} {
		if _, err := nativeLaunchArguments("codex", []string{"resume", "thread-1"}, generation, "static protocol"); err == nil {
			t.Fatal("invalid generation accepted")
		}
	}
}

func TestRestartRejectsConflictingSettingsBeforeMutation(t *testing.T) {
	for _, harness := range []string{"codex", "claude"} {
		t.Run(harness, func(t *testing.T) {
			service, runner, messageRoot := runningWorkerService(t)
			runner.absent = true
			ctx := context.Background()
			before, err := service.store.Update(
				ctx,
				"task-1",
				func(task taskstate.Task) (taskstate.Task, error) {
					snapshot := task.Snapshot()
					snapshot.Worker.Harness = harness
					snapshot.Worker.PendingGeneration = "gen-next"
					snapshot.Worker.AgentArgs = []string{"--settings", "settings.json"}
					if harness == "codex" {
						snapshot.Worker.AgentArgs = []string{
							"-c",
							"shell_environment_policy.inherit=\"all\"",
						}
					}
					return taskstate.NewMigrated(snapshot)
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			worker, err := service.SupervisedWorker(
				"task-1",
				messageRoot,
				herdr.Client{SessionRunner: runner},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.Restart(ctx); err == nil {
				t.Fatal("conflicting restart accepted")
			}
			after, err := service.store.Read(ctx, "task-1")
			if err != nil || !reflect.DeepEqual(before.Snapshot(), after.Snapshot()) ||
				runner.calls != 0 {
				t.Fatalf(
					"conflicting restart changed state: %#v, %v; calls=%d",
					after.Snapshot(),
					err,
					runner.calls,
				)
			}
			if _, err := os.Stat(messageRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("created bootstrap storage: %v", err)
			}
		})
	}
}

func TestInitialLaunchRejectsReservedPromptArguments(t *testing.T) {
	for _, harness := range []string{"codex", "claude"} {
		for _, args := range [][]string{{"caller prompt"}, {"--model", "example", "--", "caller prompt"}} {
			if err := ValidateInitialLaunch(harness, args, "assigned work"); err == nil {
				t.Errorf("accepted reserved native prompt for %s: %v", harness, args)
			}
		}
	}
}
