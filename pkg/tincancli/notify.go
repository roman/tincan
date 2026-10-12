package tincancli

import (
	"context"
	"fmt"
	"time"

	"github.com/roman/tincan/pkg/herdr"
	"github.com/roman/tincan/pkg/inbox"
	"github.com/roman/tincan/pkg/notify"
)

type NotifyCommands struct {
	Recipient string        `help:"Inbox recipient." required:""`
	Target    string        `help:"Agent name or pane ID to ring." required:""`
	Session   string        `help:"Herdr session that owns the target." env:"HERDR_SESSION" required:""`
	Interval  time.Duration `help:"Time between doorbell attempts." default:"5s"`
	Adapter   string        `help:"Terminal adapter for notifications." default:"herdr"`
	Herdr     string        `help:"Herdr executable path." default:"herdr"`
}

func selectDoorbell(adapter, herdrExecutable, session string) (notify.Doorbell, error) {
	switch adapter {
	case "herdr":
		return herdr.NewClient(herdrExecutable).ForSession(session)
	default:
		return nil, fmt.Errorf("unsupported adapter %q; supported adapter: herdr", adapter)
	}
}

func runNotify(ctx context.Context, root string, args *NotifyCommands, report func(error)) error {
	doorbell, err := selectDoorbell(args.Adapter, args.Herdr, args.Session)
	if err != nil {
		return err
	}
	store, err := inbox.New(root)
	if err != nil {
		return err
	}
	recipient, err := inbox.ParseRecipient(args.Recipient)
	if err != nil {
		return err
	}
	target, err := notify.ParseTarget(args.Target)
	if err != nil {
		return err
	}
	notifier := notify.Notifier{
		Store:     store,
		Doorbell:  doorbell,
		Recipient: recipient,
		Target:    target,
	}
	return notifier.Run(ctx, args.Interval, report)
}
