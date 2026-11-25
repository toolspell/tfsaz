package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/toolspell/tfsaz/internal/az"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	run := az.Run
	program := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	isHelper := true
	switch program {
	case "git-remote-https":
		run = az.RunHTTPSGitHelper
	case "git-remote-tfsaz":
		run = az.RunPreserveGitHelper
	default:
		isHelper = false
	}
	if err := run(ctx, os.Args[1:]); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 && (isHelper || (len(os.Args) > 2 && os.Args[1] == "route" && os.Args[2] == "run")) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "tfsaz:", err)
		os.Exit(az.ExitCode(err))
	}
}
