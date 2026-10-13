package taskops

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
)

func TestTaskNotificationsIncludeConfiguredWorkerCommands(t *testing.T) {
	for _, caller := range []string{"assignment", "resident"} {
		t.Run(caller, func(t *testing.T) {
			service, runner, messageRoot := runningWorkerService(t)
			runner.agentStatus = herdr.Idle
			recipient, _ := inbox.ParseRecipient("custom-primary")
			commands := CommandPaths{
				Concierge: "/tools/owner's commands/concierge",
				Tincan:    "/tools/transport commands/tincan",
			}
			service, err := NewWithWorkerCommands(service.root, recipient, commands)
			if err != nil {
				t.Fatal(err)
			}
			client := herdr.Client{SessionRunner: runner}
			if caller == "assignment" {
				result, err := service.SendAssignment(
					context.Background(),
					messageRoot,
					"task-1",
					"work",
					"Private assignment body",
					client,
				)
				if err != nil || result.NotificationError != nil {
					t.Fatalf("send = %+v, %v", result, err)
				}
			} else {
				worker, err := service.SupervisedWorker("task-1", messageRoot, client)
				if err != nil {
					t.Fatal(err)
				}
				if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err != nil {
					t.Fatal(err)
				}
			}
			if len(runner.promptTexts) != 1 {
				t.Fatalf("prompts = %v", runner.promptTexts)
			}
			text := runner.promptTexts[0]
			for _, fragment := range []string{
				`'/tools/owner'"'"'s commands/concierge'`, `'/tools/transport commands/tincan'`,
				"--root '" + service.root + "'", "--root '" + messageRoot + "'", "--message-root '" + messageRoot + "'", "--recipient 'custom-primary'",
				"task receive 'task-1'", "task reply send 'task-1'", "--assignment-id MESSAGE_ID", "task ack 'task-1'",
				`--generation "$CONCIERGE_SPAWN_GENERATION"`, "task-bootstrap", "inbox ack 'task-1'", "status: current",
				"Never set or export", "pending list", "missing or different",
			} {
				if !strings.Contains(text, fragment) {
					t.Errorf("notification lacks %q:\n%s", fragment, text)
				}
			}
			if strings.Contains(text, "Private assignment body") {
				t.Fatal("task content copied into notification")
			}
		})
	}
}

func TestWorkerGenerationCheckUsesInheritedValue(t *testing.T) {
	for _, inherited := range []string{"", "stale", "gen-1"} {
		command := exec.Command("sh", "-c", workerGenerationCheck("gen-1"))
		command.Env = []string{"PATH=" + os.Getenv("PATH")}
		if inherited != "" {
			command.Env = append(command.Env, "CONCIERGE_SPAWN_GENERATION="+inherited)
		}
		err := command.Run()
		if (err == nil) != (inherited == "gen-1") {
			t.Fatalf("inherited %q: %v", inherited, err)
		}
	}
}

func TestNotificationShellQuotePreservesLiteralArguments(t *testing.T) {
	for _, value := range []string{"/tools/owner's commands/concierge", `/tools/$(echo unexpected)/tincan`, "a; b", ""} {
		output, err := exec.Command("sh", "-c", "printf '%s' "+notificationShellQuote(value)).
			Output()
		if err != nil || string(output) != value {
			t.Fatalf("quoted %q = %q, %v", value, output, err)
		}
	}
}

func TestTaskNotificationRequiresConfiguredPathsWithoutLosingDelivery(t *testing.T) {
	configured, runner, messageRoot := runningWorkerService(t)
	service, err := New(configured.root)
	if err != nil {
		t.Fatal(err)
	}
	runner.agentStatus = herdr.Idle
	client := herdr.Client{SessionRunner: runner}
	result, err := service.SendAssignment(
		context.Background(),
		messageRoot,
		"task-1",
		"work",
		"Durable work",
		client,
	)
	if err != nil || result.Message.ID.String() == "" ||
		result.Notification != NotificationFailed ||
		result.NotificationError == nil {
		t.Fatalf("unconfigured notification = %+v, %v", result, err)
	}
	worker, err := service.SupervisedWorker("task-1", messageRoot, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Prompt(context.Background(), "task-1", notify.DoorbellText); err == nil {
		t.Fatal("unconfigured resident notification accepted")
	}
	if runner.prompts != 0 {
		t.Fatal("unconfigured notification submitted")
	}
	if _, err := worker.messages.Read(worker.recipient, result.Message.ID); err != nil {
		t.Fatalf("assignment lost: %v", err)
	}
}

func TestWorkerCommandPathsRequireCleanAbsolutePaths(t *testing.T) {
	recipient, _ := inbox.ParseRecipient("concierge")
	for _, paths := range []CommandPaths{
		{}, {Concierge: "relative", Tincan: "/tincan"}, {Concierge: "/concierge", Tincan: "/tools/../tincan"},
	} {
		if _, err := NewWithWorkerCommands(testRoot(t), recipient, paths); err == nil {
			t.Fatalf("invalid paths accepted: %+v", paths)
		}
	}
}
