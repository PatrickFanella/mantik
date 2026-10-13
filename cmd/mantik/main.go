package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"git.subcult.tv/subculture-collective/mantik/internal/cli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mantik:", err)
		os.Exit(1)
	}
}
