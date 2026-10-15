package main

import (
	"context"
	"os"
	"os/signal"
)

var installerEntry func() int

func main() {
	if installerEntry != nil {
		os.Exit(installerEntry())
	}
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	os.Exit(NewApp().Run(ctx, os.Args[1:]))
}
