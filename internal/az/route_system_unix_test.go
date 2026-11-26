//go:build darwin || linux

package az

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The root shell fragment must produce exactly what editRouteHosts produces.
func TestRouteHostsShellMatchesGo(t *testing.T) {
	aliases := []string{routeTestAlias}
	other := "127.0.0.1 tfsaz-aaaaaaaaaaaaaaaaaaaaaaaa.visualstudio.com # tfsaz-route ffffffffffffffff\n"
	for _, base := range []string{
		"127.0.0.1 localhost\n" + other + "10.0.0.1 server # it's quoted\n",
		"127.0.0.1 localhost\n",
		"127.0.0.1 localhost\r\n# no final newline",
	} {
		path := filepath.Join(t.TempDir(), "hosts")
		if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(path)
		run := func(install bool) string {
			t.Helper()
			script := "set -eu\n" + routeHostsShell(path, routeTestID, aliases, install)
			if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
		want := editRouteHosts(base, routeTestID, aliases, true)
		if got := run(true); got != want {
			t.Fatalf("install:\n%q\nwant\n%q", got, want)
		}
		if got := run(true); got != want {
			t.Fatalf("retry:\n%q", got)
		}
		if got := run(false); got != base {
			t.Fatalf("restore:\n%q\nwant\n%q", got, base)
		}
		if got := run(false); got != base {
			t.Fatalf("second restore:\n%q", got)
		}
		after, _ := os.Stat(path)
		if after.Mode() != info.Mode() {
			t.Fatal("the hosts file permissions changed")
		}
		if leftovers, _ := filepath.Glob(path + ".tfsaz.*"); len(leftovers) > 0 {
			t.Fatalf("temporary files left: %v", leftovers)
		}
	}
}

type routeSystemCall struct {
	name  string
	args  []string
	stdin string
}

// fakeRouteSystemCommands records commands instead of running them.
func fakeRouteSystemCommands(t *testing.T, reply func(c routeSystemCall) ([]byte, error)) *[]routeSystemCall {
	t.Helper()
	var calls []routeSystemCall
	old := runRouteSystemCommand
	runRouteSystemCommand = func(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		c := routeSystemCall{name: name, args: args, stdin: string(stdin)}
		calls = append(calls, c)
		return reply(c)
	}
	t.Cleanup(func() { runRouteSystemCommand = old })
	return &calls
}
