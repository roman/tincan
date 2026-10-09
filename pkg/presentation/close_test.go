package presentation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman/tincan/pkg/herdr"
)

type closeRunner struct {
	socket        string
	activeTab     string
	viewer        string
	paneWorkspace string
	paneTab       string
	paneExists    bool
	shiftOnClose  bool
	closeFails    bool
	focusFails    bool
	silentClose   bool
	calls         []string
}

func (runner *closeRunner) RunSession(
	_ context.Context,
	session string,
	args ...string,
) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, session+":"+strings.Join(args, " "))
	if session != "crew" || len(args) < 2 {
		return nil, nil, errors.New("wrong session or arguments")
	}
	switch args[0] + " " + args[1] {
	case "session list":
		return []byte(
			`{"sessions":[{"name":"crew","running":true,"socket_path":"` + runner.socket + `"}]}`,
		), nil, nil
	case "workspace list":
		return []byte(
			`{"result":{"workspaces":[{"workspace_id":"ws-captain","focused":true,"active_tab_id":"` + runner.activeTab + `"}]}}`,
		), nil, nil
	case "tab list":
		return []byte(
			`{"result":{"tabs":[{"tab_id":"` + runner.activeTab + `","focused":true}]}}`,
		), nil, nil
	case "tab get":
		return []byte(
			`{"result":{"tab":{"tab_id":"tab-captain","workspace_id":"ws-captain"}}}`,
		), nil, nil
	case "tab focus":
		if args[2] != "tab-captain" {
			return nil, nil, errors.New("wrong tab")
		}
		if runner.focusFails {
			return nil, nil, errors.New("focus failed")
		}
		runner.activeTab = "tab-captain"
		return []byte(`{"result":{}}`), nil, nil
	case "pane get":
		if !runner.paneExists {
			return nil, []byte(`{"error":{"code":"pane_not_found"}}`), errors.New("missing")
		}
		return []byte(
			`{"result":{"pane":{"pane_id":"pane-worker","workspace_id":"` + runner.paneWorkspace + `","tab_id":"` + runner.paneTab + `","foreground_cwd":"/tmp"}}}`,
		), nil, nil
	case "pane close":
		if runner.closeFails {
			return nil, nil, errors.New("close failed")
		}
		runner.paneExists = false
		if runner.shiftOnClose {
			runner.activeTab = "tab-other"
		}
		if runner.silentClose {
			return nil, nil, nil
		}
		return []byte(`{"result":{}}`), nil, nil
	case "terminal title":
		return []byte(`{"result":{"reason":"` + runner.viewer + `"}}`), nil, nil
	}
	return nil, nil, errors.New("unexpected command")
}

func newCloseRunner(t *testing.T) *closeRunner {
	t.Helper()
	return &closeRunner{
		socket:        filepath.Join(t.TempDir(), "crew.sock"),
		activeTab:     "tab-captain",
		paneWorkspace: "ws-worker",
		paneTab:       "tab-worker",
		paneExists:    true,
		viewer:        "cleared",
	}
}

func TestCloseBoundPanePreservesFocus(t *testing.T) {
	runner := newCloseRunner(t)
	runner.shiftOnClose = true
	namespace := filepath.Join(t.TempDir(), "locks")
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	err := closeBoundPane(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		target,
		"ws-worker",
		"tab-worker",
		func() error { return nil },
		namespace,
	)
	if err != nil || runner.paneExists || runner.activeTab != "tab-captain" {
		t.Fatalf("close err=%v pane=%v focus=%s", err, runner.paneExists, runner.activeTab)
	}
	if !containsCall(runner.calls, "crew:tab focus tab-captain --session crew") {
		t.Fatalf("focus was not restored: %v", runner.calls)
	}
}

func TestCloseBoundPaneRefusesActiveViewedTab(t *testing.T) {
	runner := newCloseRunner(t)
	runner.paneWorkspace = "ws-captain"
	runner.paneTab = "tab-captain"
	namespace := filepath.Join(t.TempDir(), "locks")
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	err := closeBoundPane(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		target,
		"ws-captain",
		"tab-captain",
		func() error { return nil },
		namespace,
	)
	if err == nil || !runner.paneExists ||
		containsCall(runner.calls, "crew:pane close pane-worker --session crew") {
		t.Fatalf("unsafe close err=%v pane=%v calls=%v", err, runner.paneExists, runner.calls)
	}
}

func TestCloseBoundPaneAllowsActiveTabWithoutViewer(t *testing.T) {
	runner := newCloseRunner(t)
	runner.paneWorkspace = "ws-captain"
	runner.paneTab = "tab-captain"
	runner.viewer = "no_foreground_client"
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	err := closeBoundPane(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		target,
		"ws-captain",
		"tab-captain",
		func() error { return nil },
		filepath.Join(t.TempDir(), "locks"),
	)
	if err != nil || runner.paneExists {
		t.Fatalf("detached viewer close err=%v pane=%v", err, runner.paneExists)
	}
}

func TestCloseBoundPaneRefusesChangedLocation(t *testing.T) {
	runner := newCloseRunner(t)
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	err := closeBoundPane(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		target,
		"ws-worker",
		"tab-other",
		func() error { return nil },
		filepath.Join(t.TempDir(), "locks"),
	)
	if err == nil || !runner.paneExists {
		t.Fatalf("changed location err=%v pane=%v", err, runner.paneExists)
	}
}

func TestCloseBoundPaneRequiresConfirmedAbsence(t *testing.T) {
	runner := newCloseRunner(t)
	runner.closeFails = true
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	err := closeBoundPane(
		context.Background(),
		herdr.Client{SessionRunner: runner},
		target,
		"ws-worker",
		"tab-worker",
		func() error { return nil },
		filepath.Join(t.TempDir(), "locks"),
	)
	if err == nil || !runner.paneExists {
		t.Fatalf("failed close err=%v pane=%v", err, runner.paneExists)
	}
}

func TestCloseBoundPaneRechecksTaskBeforeMutation(t *testing.T) {
	runner := newCloseRunner(t)
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	checks := 0
	namespace := filepath.Join(t.TempDir(), "locks")
	err := closeBoundPane(context.Background(), herdr.Client{SessionRunner: runner}, target,
		"ws-worker", "tab-worker", func() error {
			checks++
			if checks == 3 {
				return errors.New("task changed")
			}
			return nil
		}, namespace)
	if err == nil || checks != 3 || !runner.paneExists ||
		containsCall(runner.calls, "crew:pane close pane-worker --session crew") {
		t.Fatalf(
			"binding recheck err=%v checks=%d pane=%v calls=%v",
			err,
			checks,
			runner.paneExists,
			runner.calls,
		)
	}
	entries, readErr := os.ReadDir(namespace)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("preclose journal entries=%v err=%v", entries, readErr)
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func TestCloseBoundPaneRetriesFocusRestorationAfterPaneGone(t *testing.T) {
	runner := newCloseRunner(t)
	runner.shiftOnClose = true
	runner.focusFails = true
	namespace := filepath.Join(t.TempDir(), "locks")
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	first := closeBoundPane(context.Background(), herdr.Client{SessionRunner: runner}, target,
		"ws-worker", "tab-worker", func() error { return nil }, namespace)
	if first == nil || runner.paneExists {
		t.Fatalf("failed restore err=%v pane=%v", first, runner.paneExists)
	}
	entries, err := os.ReadDir(namespace)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "focus-") {
		t.Fatalf("pending focus journal=%v err=%v", entries, err)
	}
	runner.focusFails = false
	refused := closeBoundPane(context.Background(), herdr.Client{SessionRunner: runner}, target,
		"ws-worker", "tab-worker", func() error { return nil }, namespace)
	if !errors.Is(refused, ErrFocusUnsafe) || runner.activeTab != "tab-other" {
		t.Fatalf("live viewer recovery err=%v focus=%s", refused, runner.activeTab)
	}
	entries, err = os.ReadDir(namespace)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "focus-") {
		t.Fatalf("live viewer journal=%v err=%v", entries, err)
	}
	runner.viewer = "no_foreground_client"
	second := closeBoundPane(context.Background(), herdr.Client{SessionRunner: runner}, target,
		"ws-worker", "tab-worker", func() error { return nil }, namespace)
	if second != nil || runner.activeTab != "tab-captain" {
		t.Fatalf("retry err=%v focus=%s", second, runner.activeTab)
	}
	entries, err = os.ReadDir(namespace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("focus journal remains=%v err=%v", entries, err)
	}
	count := 0
	for _, call := range runner.calls {
		if call == "crew:pane close pane-worker --session crew" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("pane close count=%d calls=%v", count, runner.calls)
	}
}

func TestCloseBoundPaneAcceptsSilentSuccessfulClose(t *testing.T) {
	runner := newCloseRunner(t)
	runner.silentClose = true
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	err := closeBoundPane(context.Background(), herdr.Client{SessionRunner: runner}, target,
		"ws-worker", "tab-worker", func() error { return nil }, filepath.Join(t.TempDir(), "locks"))
	if err != nil || runner.paneExists {
		t.Fatalf("silent close err=%v pane=%v", err, runner.paneExists)
	}
}

func TestCloseBoundPaneDiscardsPreparedJournalWhenPaneStillExists(t *testing.T) {
	runner := newCloseRunner(t)
	runner.activeTab = "tab-other"
	namespace := filepath.Join(t.TempDir(), "locks")
	if err := securePresentationNamespace(namespace); err != nil {
		t.Fatal(err)
	}
	target, _ := herdr.ParseTarget("crew", "pane-worker")
	path := journalPath(namespace, target, runner.socket)
	if _, err := publishCloseJournal(path, closeJournal{
		Session: "crew", Pane: "pane-worker", Socket: runner.socket,
		Workspace: "ws-worker", Tab: "tab-worker", Focus: focus{"ws-captain", "tab-captain"},
	}); err != nil {
		t.Fatal(err)
	}
	err := closeBoundPane(context.Background(), herdr.Client{SessionRunner: runner}, target,
		"ws-worker", "tab-worker", func() error { return nil }, namespace)
	if err != nil || runner.paneExists || runner.activeTab != "tab-other" {
		t.Fatalf(
			"prepared journal recovery err=%v pane=%v focus=%s",
			err,
			runner.paneExists,
			runner.activeTab,
		)
	}
	if containsCall(runner.calls, "crew:tab focus tab-captain --session crew") {
		t.Fatalf("stale focus was restored: %v", runner.calls)
	}
}
