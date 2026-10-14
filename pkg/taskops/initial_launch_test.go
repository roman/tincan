package taskops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskstate"
	"github.com/roman/tincan/pkg/worktree"
)

type initialLaunchRunner struct {
	service          *Service
	intent           taskstate.LaunchIntent
	created, started bool
	creates, starts  int
	failure          string
	startArgs        []string
}

func (runner *initialLaunchRunner) RunSession(
	ctx context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	if session != runner.intent.Session {
		return nil, nil, fmt.Errorf("wrong session: %s", session)
	}
	switch strings.Join(args[:2], " ") {
	case "tab create":
		task, err := runner.service.store.Read(ctx, "task-1")
		if err != nil || task.Snapshot().LaunchIntent.Phase != taskstate.LaunchPaneRequested {
			return nil, nil, errors.New("pane effect precedes durable phase")
		}
		runner.creates++
		runner.created = runner.failure != "pane absent"
		if strings.HasPrefix(runner.failure, "pane") {
			return nil, nil, errors.New("creation response lost")
		}
		return []byte(
			fmt.Sprintf(
				`{"result":{"tab":{"workspace_id":"w1","tab_id":"w1:t2","label":%q},"root_pane":{"pane_id":"w1:p2"}}}`,
				runner.intent.Label,
			),
		), nil, nil
	case "tab list":
		if !runner.created {
			return []byte(`{"result":{"tabs":[]}}`), nil, nil
		}
		return []byte(
			fmt.Sprintf(
				`{"result":{"tabs":[{"workspace_id":"w1","tab_id":"w1:t2","label":%q}]}}`,
				runner.intent.Label,
			),
		), nil, nil
	case "pane list":
		return []byte(
			`{"result":{"panes":[{"workspace_id":"w1","tab_id":"w1:t2","pane_id":"w1:p2"}]}}`,
		), nil, nil
	case "pane get":
		return []byte(
			fmt.Sprintf(
				`{"result":{"pane":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t2","foreground_cwd":%q}}}`,
				runner.intent.Worktree,
			),
		), nil, nil
	case "agent get":
		if !runner.started {
			return nil, []byte(`{"error":{"code":"agent_not_found"}}`), errors.New("exit 1")
		}
		return []byte(
			fmt.Sprintf(
				`{"result":{"agent":{"pane_id":"w1:p2","agent_status":"idle","agent":%q,"agent_session":{"source":%q,"agent":%q,"kind":"id","value":"native-session"}}}}`,
				runner.intent.Harness,
				"herdr:"+runner.intent.Harness,
				runner.intent.Harness,
			),
		), nil, nil
	case "agent start":
		runner.startArgs = append([]string(nil), args...)
		task, err := runner.service.store.Read(ctx, "task-1")
		if err != nil || task.Snapshot().LaunchIntent.Phase != taskstate.LaunchAgentRequested {
			return nil, nil, errors.New("agent effect precedes durable phase")
		}
		runner.starts++
		runner.started = runner.failure != "agent absent"
		if strings.HasPrefix(runner.failure, "agent") {
			return nil, nil, errors.New("startup response lost")
		}
		return []byte(`{"result":{}}`), nil, nil
	case "pane process-info":
		return []byte(
			fmt.Sprintf(
				`{"result":{"type":"pane_process_info","process_info":{"pane_id":"w1:p2","shell_pid":100,"foreground_processes":[{"pid":101,"name":%q,"argv0":%q}]}}}`,
				runner.intent.Harness,
				runner.intent.Harness,
			),
		), nil, nil
	case "agent prompt":
		return []byte(`{"result":{}}`), nil, nil
	default:
		return nil, nil, fmt.Errorf("unexpected command: %v", args)
	}
}

func initialLaunchFixture(t *testing.T) (*Service, *initialLaunchRunner, InitialLaunchConfig) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", project}, args...)...).
			CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git(
		"-c",
		"user.name=Test",
		"-c",
		"user.email=test@example.invalid",
		"commit",
		"--allow-empty",
		"-m",
		"Initial",
	)
	recipient, _ := inbox.ParseRecipient(DefaultPrimaryRecipient)
	service, err := NewWithWorkerCommands(
		testRoot(t),
		recipient,
		CommandPaths{Concierge: "/tools/concierge", Tincan: "/tools/tincan"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), taskstate.Definition{ID: "task-1", Title: "Work", Repo: "project", Kind: "ship"}); err != nil {
		t.Fatal(err)
	}
	intent := taskstate.LaunchIntent{
		Generation: "gen-1",
		Harness:    "codex",
		Name:       "worker-1",
		Session:    "work",
		Project:    project,
		Worktree:   filepath.Join(root, "worker"),
		Branch:     "task/work",
		BaseCommit: git("rev-parse", "HEAD"),
		Workspace:  "w1",
		Label:      "concierge-gen-1",
		Body:       "Do the requested work.",
	}
	if _, err := service.store.ReserveInitialLaunch(context.Background(), "task-1", intent); err != nil {
		t.Fatal(err)
	}
	runner := &initialLaunchRunner{service: service, intent: intent}
	executable, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	return service, runner, InitialLaunchConfig{
		ReserveEndpoint: func(taskstate.LaunchEndpoint) error { return nil },
		Git:             worktree.Client{Executable: executable},
		Herdr:           herdr.Client{SessionRunner: runner},
		MessageRoot:     filepath.Join(root, "messages"),
	}
}

func TestInitialLaunchPublishesAssignmentBeforeClearingIntent(t *testing.T) {
	service, runner, config := initialLaunchFixture(t)
	task, err := service.ContinueInitialLaunch(context.Background(), "task-1", "gen-1", config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := task.Snapshot()
	if snapshot.State != taskstate.InFlight || snapshot.LaunchIntent != nil ||
		snapshot.Worker == nil ||
		snapshot.Worker.PendingAdoption {
		t.Fatalf("launch = %+v", snapshot)
	}
	if strings.Join(snapshot.Worker.AgentArgs, " ") != "resume native-session" {
		t.Fatalf("resume = %v", snapshot.Worker.AgentArgs)
	}
	if runner.creates != 1 || runner.starts != 1 {
		t.Fatalf("effects = %d, %d", runner.creates, runner.starts)
	}
	messages, err := inbox.New(config.MessageRoot)
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("task-1")
	pending, err := messages.List(recipient)
	if err != nil || len(pending) != 1 || !strings.Contains(pending[0].Body, runner.intent.Body) {
		t.Fatalf("assignment = %+v, %v", pending, err)
	}
}

func TestInitialLaunchCapturesClaudeResumeIdentity(t *testing.T) {
	service, runner, config := initialLaunchFixture(t)
	ctx := context.Background()
	if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.CancelInitialLaunch("gen-1") }); err != nil {
		t.Fatal(err)
	}
	runner.intent.Harness = "claude"
	if _, err := service.store.ReserveInitialLaunch(ctx, "task-1", runner.intent); err != nil {
		t.Fatal(err)
	}
	task, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
	if err != nil {
		t.Fatal(err)
	}
	worker := task.Snapshot().Worker
	if task.State() != taskstate.InFlight || task.Snapshot().LaunchIntent != nil || worker == nil ||
		worker.Harness != "claude" ||
		strings.Join(worker.AgentArgs, " ") != "--resume native-session" {
		t.Fatalf("Claude launch: %+v", task.Snapshot())
	}
}

func TestInitialLaunchRequiresEndpointReservation(t *testing.T) {
	service, runner, config := initialLaunchFixture(t)
	config.ReserveEndpoint = nil
	if _, err := service.ContinueInitialLaunch(context.Background(), "task-1", "gen-1", config); err == nil {
		t.Fatal("launch accepted a missing endpoint reservation")
	}
	if runner.creates != 0 || runner.starts != 0 {
		t.Fatal("launch performed an effect without a reservation provider")
	}
}

func TestInitialLaunchRejectsInvalidStoredIntentBeforeEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*taskstate.LaunchIntent)
	}{
		{"blank body", func(intent *taskstate.LaunchIntent) { intent.Body = " \t\n" }},
		{"conflicting arguments", func(intent *taskstate.LaunchIntent) {
			intent.AgentArgs = []string{"-c", "shell_environment_policy.inherit=all"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, runner, config := initialLaunchFixture(t)
			ctx := context.Background()
			if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.CancelInitialLaunch("gen-1") }); err != nil {
				t.Fatal(err)
			}
			intent := runner.intent
			test.change(&intent)
			if _, err := service.store.ReserveInitialLaunch(ctx, "task-1", intent); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); err == nil {
				t.Fatal("invalid stored intent executed")
			}
			task, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			if task.Snapshot().LaunchIntent.Phase != taskstate.LaunchReserved ||
				runner.creates != 0 ||
				runner.starts != 0 {
				t.Fatal("invalid intent advanced")
			}
		})
	}
}

func TestInitialLaunchEndpointContentionPreventsAgentStart(t *testing.T) {
	service, runner, config := initialLaunchFixture(t)
	ctx := context.Background()
	busy := errors.New("endpoint reserved by manual control")
	config.ReserveEndpoint = func(endpoint taskstate.LaunchEndpoint) error { return busy }
	if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); !errors.Is(
		err,
		busy,
	) {
		t.Fatalf("contention: %v", err)
	}
	task, err := service.store.Read(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Snapshot().LaunchIntent.Endpoint != nil || runner.creates != 1 || runner.starts != 0 {
		t.Fatal("launch published or started a contested endpoint")
	}
	config.ReserveEndpoint = func(endpoint taskstate.LaunchEndpoint) error { return nil }
	if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); err != nil {
		t.Fatal(err)
	}
	if runner.creates != 1 || runner.starts != 1 {
		t.Fatal("retry repeated pane creation")
	}
}

func TestInitialLaunchReservesRecordedEndpointBeforeInspection(t *testing.T) {
	service, runner, config := initialLaunchFixture(t)
	ctx := context.Background()
	runner.failure = "agent response"
	if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); err == nil {
		t.Fatal("lost response reported success")
	}
	busy := errors.New("endpoint busy")
	config.ReserveEndpoint = func(endpoint taskstate.LaunchEndpoint) error { return busy }
	if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); !errors.Is(
		err,
		busy,
	) {
		t.Fatalf("recorded endpoint bypassed reservation: %v", err)
	}
	task, err := service.store.Read(ctx, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Snapshot().LaunchIntent.ResumeArgs) != 0 || runner.starts != 1 {
		t.Fatal("contested endpoint advanced")
	}
}

func TestInitialLaunchRecoversUncertainEffectsWithoutRepeatingThem(t *testing.T) {
	for _, failure := range []string{"pane response", "pane absent", "agent response", "agent absent"} {
		t.Run(failure, func(t *testing.T) {
			service, runner, config := initialLaunchFixture(t)
			runner.failure = failure
			if _, err := service.ContinueInitialLaunch(context.Background(), "task-1", "gen-1", config); err == nil {
				t.Fatal("uncertain effect reported success")
			}
			runner.failure = ""
			_, err := service.ContinueInitialLaunch(context.Background(), "task-1", "gen-1", config)
			if strings.HasSuffix(failure, "absent") && err == nil {
				t.Fatal("missing uncertain effect was recreated")
			}
			if strings.HasSuffix(failure, "response") && err != nil {
				t.Fatal(err)
			}
			if runner.creates != 1 || runner.starts > 1 {
				t.Fatalf("effects repeated: %d, %d", runner.creates, runner.starts)
			}
		})
	}
}

func TestInitialLaunchRecordsExistingEffectsWhileHeld(t *testing.T) {
	for _, failure := range []string{"pane response", "agent response"} {
		t.Run(failure, func(t *testing.T) {
			service, runner, config := initialLaunchFixture(t)
			runner.failure = failure
			ctx := context.Background()
			if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); err == nil {
				t.Fatal("lost response reported success")
			}
			if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) {
				return task.PlaceHold(taskstate.Hold{Reason: "Wait"})
			}); err != nil {
				t.Fatal(err)
			}
			runner.failure = ""
			creates, starts := runner.creates, runner.starts
			if _, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config); err == nil {
				t.Fatal("held launch advanced to dispatch")
			}
			task, err := service.store.Read(ctx, "task-1")
			if err != nil {
				t.Fatal(err)
			}
			snapshot := task.Snapshot()
			if snapshot.LaunchIntent.Endpoint == nil {
				t.Fatal("existing endpoint was not recorded under hold")
			}
			if failure == "agent response" && len(snapshot.LaunchIntent.ResumeArgs) == 0 {
				t.Fatal("existing native identity was not recorded under hold")
			}
			if snapshot.State != taskstate.Queued || snapshot.Worker != nil ||
				runner.creates != creates ||
				runner.starts != starts {
				t.Fatalf("held launch performed another effect: %+v", snapshot)
			}
		})
	}
}

func TestInitialLaunchSuppliesStaticWorkerProtocol(t *testing.T) {
	for _, harness := range []string{"codex", "claude"} {
		t.Run(harness, func(t *testing.T) {
			service, runner, config := initialLaunchFixture(t)
			ctx := context.Background()
			if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.CancelInitialLaunch("gen-1") }); err != nil {
				t.Fatal(err)
			}
			runner.intent.Harness = harness
			runner.intent.AgentArgs = []string{"--model", "custom-model"}
			if harness == "codex" {
				runner.intent.AgentArgs = append(
					runner.intent.AgentArgs,
					"--profile",
					"worker-profile",
				)
			} else {
				runner.intent.AgentArgs = append(runner.intent.AgentArgs, "--append-system-prompt", "worker context")
			}

			runner.intent.Body = "private-assignment-canary"
			if _, err := service.store.ReserveInitialLaunch(ctx, "task-1", runner.intent); err != nil {
				t.Fatal(err)
			}
			task, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
			if err != nil {
				t.Fatal(err)
			}
			protocol := runner.startArgs[len(runner.startArgs)-1]
			for _, required := range []string{"Concierge worker", "await", "/tools/concierge", "/tools/tincan", "task receive", "task reply send", "CONCIERGE_SPAWN_GENERATION", "standalone"} {
				if !strings.Contains(protocol, required) {
					t.Errorf("native protocol lacks %q: %s", required, protocol)
				}
			}
			if strings.ContainsAny(protocol, "\r\n") || strings.Contains(protocol, "gen-1") ||
				strings.Contains(protocol, runner.intent.Body) {
				t.Fatalf(
					"protocol contains transport-unsafe or generation/assignment text: %q",
					protocol,
				)
			}
			resume := "resume"
			if harness == "claude" {
				resume = "--resume"
			}
			want := append(
				append([]string(nil), runner.intent.AgentArgs...),
				resume,
				"native-session",
			)
			if got := runner.startArgs[len(runner.startArgs)-1-len(runner.intent.AgentArgs) : len(runner.startArgs)-1]; !reflect.DeepEqual(
				got,
				runner.intent.AgentArgs,
			) {
				t.Fatalf("launch options changed: %v", got)
			}
			if got := task.Snapshot().Worker.AgentArgs; !reflect.DeepEqual(got, want) {
				t.Fatalf("reusable arguments changed: %v", got)
			}
			bound := task.Snapshot().Worker
			restarted := &workerRunner{
				worktree: bound.Worktree,
				pane:     bound.Pane,
				harness:  harness,
				absent:   true,
			}
			worker, err := service.SupervisedWorker(
				"task-1",
				config.MessageRoot,
				herdr.Client{SessionRunner: restarted},
			)
			if err != nil {
				t.Fatal(err)
			}
			for restart := 0; restart < 2; restart++ {
				restarted.absent = true
				if err := worker.Restart(ctx); err != nil {
					t.Fatal(err)
				}
				loaded, err := service.store.Read(ctx, "task-1")
				if err != nil {
					t.Fatal(err)
				}
				if got := loaded.Snapshot().Worker.AgentArgs; !reflect.DeepEqual(got, want) {
					t.Fatalf("restart changed stored options: %v", got)
				}
				arguments := restarted.startArgs
				if len(arguments) != 10+len(want)+1 ||
					!reflect.DeepEqual(arguments[10:len(arguments)-1], want) ||
					arguments[len(arguments)-1] != protocol {
					t.Fatalf("restart dropped or duplicated caller options/protocol: %v", arguments)
				}
				if !strings.Contains(arguments[9], loaded.Snapshot().Worker.Generation) {
					t.Fatalf("restart lost its new generation: %v", arguments[:10])
				}
			}

		})
	}
}

func TestInterruptedLaunchRestoresCallerOptions(t *testing.T) {
	for _, harness := range []string{"codex", "claude"} {
		for _, phase := range []string{"native recorded", "adoption reserved", "adoption confirmed", "worker started"} {
			t.Run(harness+"/"+phase, func(t *testing.T) {
				service, runner, config := initialLaunchFixture(t)
				ctx := context.Background()
				if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.CancelInitialLaunch("gen-1") }); err != nil {
					t.Fatal(err)
				}
				runner.intent.Harness = harness
				runner.intent.AgentArgs = []string{"--model", "custom-model"}
				if _, err := service.store.ReserveInitialLaunch(ctx, "task-1", runner.intent); err != nil {
					t.Fatal(err)
				}
				if err := config.Git.Prepare(ctx, launchWorktree(&runner.intent)); err != nil {
					t.Fatal(err)
				}
				for _, advance := range []func(taskstate.Task) (taskstate.Task, error){
					func(task taskstate.Task) (taskstate.Task, error) { return task.BeginLaunchPreparation("gen-1") },
					func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchPane("gen-1") },
				} {
					if _, err := service.store.Update(ctx, "task-1", advance); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := service.store.RecordLaunchEndpoint(ctx, "task-1", "gen-1", taskstate.LaunchEndpoint{Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"}); err != nil {
					t.Fatal(err)
				}
				selector := "resume"
				if harness == "claude" {
					selector = "--resume"
				}
				bare := []string{selector, "native-session"}
				for _, advance := range []func(taskstate.Task) (taskstate.Task, error){
					func(task taskstate.Task) (taskstate.Task, error) { return task.RequestLaunchAgent("gen-1") },
					func(task taskstate.Task) (taskstate.Task, error) { return task.RecordLaunchResume("gen-1", bare) },
				} {
					if _, err := service.store.Update(ctx, "task-1", advance); err != nil {
						t.Fatal(err)
					}
				}
				if phase != "native recorded" {
					if _, err := service.store.ReserveAdoption(ctx, "task-1", taskstate.Worker{Name: runner.intent.Name, Harness: harness, Session: "work", Pane: "w1:p2", Project: runner.intent.Project, Worktree: runner.intent.Worktree, Generation: "gen-1", AgentArgs: bare}); err != nil {
						t.Fatal(err)
					}
					if phase == "adoption confirmed" || phase == "worker started" {
						if _, err := service.store.Update(ctx, "task-1", func(task taskstate.Task) (taskstate.Task, error) { return task.ConfirmAdoption("w1", "w1:t2") }); err != nil {
							t.Fatal(err)
						}
					}
				}
				if phase == "worker started" {
					if _, err := service.Start(ctx, "task-1"); err != nil {
						t.Fatal(err)
					}
				}
				runner.created, runner.started = true, true
				task, err := service.ContinueInitialLaunch(ctx, "task-1", "gen-1", config)
				if err != nil {
					t.Fatal(err)
				}
				want := append(append([]string(nil), runner.intent.AgentArgs...), bare...)
				if got := task.Snapshot().Worker.AgentArgs; !reflect.DeepEqual(got, want) {
					t.Fatalf("interrupted launch lost options: %v", got)
				}
				if task.Snapshot().LaunchIntent != nil || runner.creates != 0 ||
					runner.starts != 0 {
					t.Fatal("recovery repeated native effects or retained intent")
				}
			})
		}
	}
}
