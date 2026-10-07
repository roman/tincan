package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
)

type cli struct {
	Root      string        `help:"Inbox state directory." required:"" type:"path"`
	Recipient string        `help:"Inbox recipient." required:""`
	Target    string        `help:"Agent name or pane ID to ring." required:""`
	Interval  time.Duration `help:"Time between doorbell attempts." default:"5s"`
	Adapter   string        `help:"Terminal adapter for notifications." default:"herdr"`
	Herdr     string        `help:"Herdr executable path." default:"herdr"`
}

func selectDoorbell(adapter, herdrExecutable string) (notify.Doorbell, error) {
	switch adapter {
	case "herdr":
		return herdr.NewClient(herdrExecutable), nil
	default:
		return nil, fmt.Errorf("unsupported adapter %q; supported adapter: herdr", adapter)
	}
}

func main() {
	var args cli
	kong.Parse(&args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report := func(err error) { fmt.Fprintln(os.Stderr, "tincan-notify:", err) }
	doorbell, err := selectDoorbell(args.Adapter, args.Herdr)
	if err != nil {
		report(err)
		os.Exit(1)
	}
	store, err := inbox.New(args.Root)
	if err != nil {
		report(err)
		os.Exit(1)
	}
	recipient, err := inbox.ParseRecipient(args.Recipient)
	if err != nil {
		report(err)
		os.Exit(1)
	}
	target, err := notify.ParseTarget(args.Target)
	if err != nil {
		report(err)
		os.Exit(1)
	}
	notifier := notify.Notifier{
		Store:     store,
		Doorbell:  doorbell,
		Recipient: recipient,
		Target:    target,
	}
	err = notifier.Run(ctx, args.Interval, report)
	if err != nil {
		report(err)
		os.Exit(1)
	}
}
