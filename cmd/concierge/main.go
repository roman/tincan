package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/roman/tincan/pkg/conciergecli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(conciergecli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
