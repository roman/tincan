package hostservice

import (
	"fmt"
	"github.com/roman/tincan/pkg/supervise"
	"io"

	"github.com/roman/tincan/pkg/firstmate"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/taskops"
)

// Config binds every external dependency before host admission starts.
type Config struct {
	Supervision     supervise.Config
	Diagnostics     io.Writer
	Root            string
	MessageRoot     string
	Recipient       inbox.Recipient
	HerdrExecutable string
	GitExecutable   string
	WorkerCommands  taskops.CommandPaths
	Session         string
}

// Expectations reject conflicting client settings without selecting host resources.
type Expectations struct {
	MessageRoot     string `json:"message_root,omitempty"`
	Recipient       string `json:"recipient,omitempty"`
	HerdrExecutable string `json:"herdr_executable,omitempty"`
	Session         string `json:"session,omitempty"`
}

func (config Config) validate() error {
	if config.GitExecutable != "" {
		if _, err := firstmate.ParseAbsolutePath(config.GitExecutable); err != nil {
			return err
		}
	}
	if err := validateSupervisionConfig(config.Supervision); err != nil {
		return err
	}
	for _, path := range []string{config.Root, config.MessageRoot, config.HerdrExecutable} {
		if _, err := firstmate.ParseAbsolutePath(path); err != nil {
			return err
		}
	}
	if _, err := inbox.ParseRecipient(config.Recipient.String()); err != nil {
		return err
	}
	if config.Session != "" {
		if _, err := herdr.NewClient(config.HerdrExecutable).ForSession(config.Session); err != nil {
			return err
		}
	}
	return nil
}

func (server *Server) checkExpectations(expected *Expectations) error {
	if expected == nil {
		return nil
	}
	for _, field := range []struct{ name, expected, configured string }{
		{"message root", expected.MessageRoot, server.config.MessageRoot},
		{"recipient", expected.Recipient, server.config.Recipient.String()},
		{"Herdr executable", expected.HerdrExecutable, server.config.HerdrExecutable},
		{"Herdr session", expected.Session, server.config.Session},
	} {
		if field.expected != "" && field.expected != field.configured {
			return fmt.Errorf("client %s conflicts with host configuration", field.name)
		}
	}
	return nil
}
