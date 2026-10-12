package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/roman/tincan/pkg/tincancli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(tincancli.RunNotify(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
