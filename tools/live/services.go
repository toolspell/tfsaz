package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func (e *env) composeArgs(args []string) []string {
	return append([]string{"compose", "-p", e.project(), "-f", filepath.Join(e.bed, "compose.yaml")}, args...)
}

func (e *env) compose(args ...string) error {
	if e.network == nil {
		return fmt.Errorf("instance %s has no slot; run go run ./tools/live up", e.instance)
	}
	return cmd(e.bed, e.network.composeEnv(e.project(), e.instancePath()), "docker", e.composeArgs(args)...)
}

func (e *env) composeDown() error {
	if err := e.compose("down", "--remove-orphans", "--rmi", "local"); err != nil {
		return fmt.Errorf("stop %s: %w; check the Compose output and retry", e.project(), err)
	}
	return nil
}

func (e *env) serviceRunning(service string) (bool, error) {
	if e.network == nil {
		return false, nil
	}
	out, err := output(e.network.composeEnv(e.project(), e.instancePath()), "docker", e.composeArgs(
		[]string{"ps", "--status", "running", "-q", service})...)
	return out != "", err
}

func (e *env) prepareBed() error {
	for _, dir := range []string{e.instancePath("router", "peers"), e.instancePath("mock-keys")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	config, err := e.network.corefile(filepath.Join(e.bed, "dns", "Corefile"))
	if err != nil {
		return err
	}
	path := e.instancePath("Corefile")
	data, err := os.ReadFile(path)
	if err == nil && string(data) == config {
		return nil
	}
	return os.WriteFile(path, []byte(config), 0o600)
}

func (e *env) services(action string) error {
	switch action {
	case "up":
		networks, err := networkSnapshot()
		if err != nil {
			return err
		}
		mockRunning, err := e.serviceRunning("devops-server-mock")
		if err != nil {
			return err
		}
		routerRunning, err := e.serviceRunning("router")
		if err != nil {
			return err
		}
		if err := e.reserveNetwork(networks, func(ports bedPorts) error {
			return probeHostPorts(ports, mockRunning, routerRunning)
		}); err != nil {
			return err
		}
		if err := e.prepareBed(); err != nil {
			return err
		}
		step("starting %s on %s", e.project(), e.network.subnet)
		if err := e.compose("up", "-d", "--build", "--wait", "--wait-timeout", "60"); err != nil {
			return fmt.Errorf("start %s: %w; check the Compose output, then retry up or run down to release the slot", e.project(), err)
		}
		return e.waitMock()
	case "down":
		if e.network == nil {
			return nil
		}
		if err := e.composeDown(); err != nil {
			return err
		}
		networks, err := output(nil, "docker", "network", "ls", "--filter", "label=com.docker.compose.project="+e.project(),
			"--filter", "label=com.docker.compose.network=bed", "--quiet")
		if err != nil {
			return err
		}
		if networks != "" {
			return fmt.Errorf("%s still has a Docker network; detach any additional containers from it and retry down", e.project())
		}
		return nil
	case "status":
		fmt.Printf("instance %s\n", e.instance)
		if e.network == nil {
			fmt.Println("  no slot reserved")
			return nil
		}
		fmt.Printf("  subnet %s, host ports HTTP %d, HTTPS %d, SSH %d, journal %d, WireGuard UDP %d\n",
			e.network.subnet, e.network.ports.HTTP, e.network.ports.HTTPS, e.network.ports.SSH, e.network.ports.Journal, e.network.ports.WireGuard)
		return e.compose("ps", "--format", "  {{.Service}}\t{{.State}}")
	}
	return fmt.Errorf("unknown services action %q", action)
}

func (e *env) waitMock() error {
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := requestMockJournal(http.MethodGet, e.network.journalURL(), http.StatusOK)
		if err == nil {
			return resp.Body.Close()
		}
		last = err
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("%s did not become ready: %w; check docker compose output and retry up", e.project(), last)
}

func (e *env) requireRouter() error {
	running, err := e.serviceRunning("router")
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("the router is not running; run go run ./tools/live up")
	}
	_, err = e.routerKey()
	return err
}
