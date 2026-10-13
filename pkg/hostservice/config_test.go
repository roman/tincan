package hostservice

import (
	"context"
	"errors"
	"github.com/roman/tincan/pkg/primary"
	"github.com/roman/tincan/pkg/taskstate"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman/tincan/pkg/inbox"
)

func TestHostConfigurationBindsExternalDependencies(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	recipient, _ := inbox.ParseRecipient("concierge")
	config := Config{
		Root:            root,
		MessageRoot:     filepath.Join(root, "messages"),
		Recipient:       recipient,
		HerdrExecutable: executable,
		Session:         "work",
	}
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.checkExpectations(&Expectations{MessageRoot: config.MessageRoot, Recipient: "concierge", HerdrExecutable: executable, Session: "work"}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []Expectations{
		{MessageRoot: filepath.Join(root, "different")}, {Recipient: "firstmate"}, {HerdrExecutable: "/different/herdr"}, {Session: "other"},
	} {
		if err := server.checkExpectations(&expected); err == nil {
			t.Fatalf("accepted conflicting expectations: %+v", expected)
		}
	}
	if err := server.checkExpectations(nil); err != nil {
		t.Fatal(err)
	}
}

func TestHostConfigurationRejectsUnboundDependencies(t *testing.T) {
	root := t.TempDir()
	executable, _ := os.Executable()
	recipient, _ := inbox.ParseRecipient("concierge")
	config := Config{
		Root:            root,
		MessageRoot:     filepath.Join(root, "messages"),
		Recipient:       recipient,
		HerdrExecutable: executable,
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Root = "relative" },
		func(c *Config) { c.MessageRoot = "relative" },
		func(c *Config) { c.HerdrExecutable = "herdr" },
		func(c *Config) { c.Session = "invalid/session" },
	} {
		candidate := config
		change(&candidate)
		if _, err := New(candidate); err == nil {
			t.Fatalf("accepted invalid config: %+v", candidate)
		}
	}
}

func TestConflictingExpectationsFailBeforeAdmissionAndWrites(t *testing.T) {
	_, root, socket := fixture(t, func(server *Server) {
		server.authorize = func(primary.Caller) error { t.Error("conflict reached caller admission"); return nil }
	})
	request := intakeRequest()
	request.Expectations = &Expectations{Recipient: "other"}
	response, err := Call(context.Background(), socket, request)
	if err != nil || response.Outcome != Invalid {
		t.Fatalf("response %+v error %v", response, err)
	}
	store, _ := taskstate.NewStore(root)
	tasks, err := store.List(context.Background())
	if err != nil || len(tasks) != 0 {
		t.Fatalf("conflict wrote tasks: %+v %v", tasks, err)
	}
	if _, err := os.Stat(filepath.Join(root, "backlog.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("conflict wrote projection: %v", err)
	}
}
