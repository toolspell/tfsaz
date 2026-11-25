package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/toolspell/tfsaz/internal/az"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := az.Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tfsaz:", err)
		os.Exit(az.ExitCode(err))
	}
}
