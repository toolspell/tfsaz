package main

import (
	"fmt"
	"path/filepath"
	"time"
)

func (e *env) compose(args ...string) error {
	return cmd(e.bed, nil, "docker", append([]string{"compose", "-f", filepath.Join(e.bed, "compose.yaml")}, args...)...)
}

func (e *env) routerRunning() (bool, error) {
	out, err := output(nil, "docker", "compose", "-f", filepath.Join(e.bed, "compose.yaml"), "ps", "--status", "running", "-q", "router")
	return out != "", err
}

// services starts, stops or shows the Docker test bed.
func (e *env) services(action string) error {
	switch action {
	case "up":
		if err := e.ensureHome(); err != nil {
			return err
		}
		step("starting the test bed")
		if err := e.compose("up", "-d", "--build", "--wait"); err != nil {
			return err
		}
		// The router writes its public key on start.
		var err error
		for range 20 {
			if _, err = e.routerKey(); err == nil {
				return nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		return fmt.Errorf("wait for router public key: %w", err)
	case "down":
		return e.compose("down")
	case "status":
		return e.compose("ps")
	}
	return fmt.Errorf("unknown services action %q", action)
}

func (e *env) requireRouter() error {
	running, err := e.routerRunning()
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("the router is stopped; start it with go run ./tools/live services up")
	}
	_, err = e.routerKey()
	return err
}
