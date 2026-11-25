// The Windows fixtures need an executable that can accept native process
// arguments before invoking the shell scripts shared with the Unix tests.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmd := exec.Command(os.Getenv("TFSAZ_TEST_SHELL"), append([]string{exe + ".sh"}, os.Args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
