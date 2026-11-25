//go:build darwin || linux

package az

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

const routeHostsCRLF = false

func routeHostsPath() string { return "/etc/hosts" }

func routeSystemOwnedByUser(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return errors.New("the routing directory must belong to the current user")
	}
	return nil
}

// routeSystemRootTool accepts the first candidate that is a root-owned
// executable nobody else can replace. Privileged scripts call it by that path.
func routeSystemRootTool(candidates ...string) (string, error) {
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if ok && st.Uid == 0 && info.Mode().IsRegular() && info.Mode().Perm()&0o022 == 0 && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s is missing or not a root-owned system program", candidates[0])
}

// runRouteSystemPrivileged runs one fixed script through sudo. The script is
// built in memory from validated values and calls system programs only; it
// never runs tfsaz or reads a file the user can write.
func runRouteSystemPrivileged(ctx context.Context, script string) error {
	sudo, err := routeSystemRootTool("/usr/bin/sudo", "/bin/sudo")
	if err != nil {
		return fmt.Errorf("browser routing needs sudo for the hosts file and port 443: %w", err)
	}
	prompt := "tfsaz needs administrator rights to change the hosts file and loopback port 443. Password for %u: "
	_, err = runRouteSystemCommand(ctx, nil, sudo, "-p", prompt, "--", "/bin/sh", "-c", "set -eu\numask 022\nPATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\n"+script)
	if err != nil {
		return fmt.Errorf("administrator step: %w (sudo needs an interactive terminal or cached credentials)", err)
	}
	return nil
}

// writeRootFile is a shell fragment that replaces path with root-owned content
// through a temporary file, so an interruption never leaves a partial file.
func writeRootFile(path, content, group string) string {
	if strings.Contains(content, "\nTFSAZ_EOF\n") {
		panic("heredoc terminator in content")
	}
	q := shellQuote(path)
	return fmt.Sprintf(`f=%s
tmp=$(mktemp "$f.tfsaz.XXXXXX")
trap 'rm -f "$tmp"' EXIT
cat > "$tmp" <<'TFSAZ_EOF'
%sTFSAZ_EOF
chown root:%s "$tmp"
chmod 644 "$tmp"
mv -f "$tmp" "$f"
trap - EXIT
`, q, content, group)
}

const routePortOwner = "/etc/tfsaz-browser-port-443.owner"

// Port forwarding is shared by all user sessions, including while the user's
// redirect process is stopped. Reserve it independently of listener readiness.
func reserveRoutePort(id string) string {
	return fmt.Sprintf(`f=%s
if [ ! -e "$f" ]; then
  tmp=$(mktemp "$f.XXXXXX")
  trap 'rm -f "$tmp"' EXIT
  printf '%%s\n' %s > "$tmp"
  chmod 644 "$tmp"
  ln "$tmp" "$f" 2>/dev/null || true
  rm -f "$tmp"; trap - EXIT
fi
[ ! -L "$f" ] && [ "$(cat "$f")" = %s ] || { echo "loopback port 443 is reserved by another managed installation" >&2; exit 1; }
`, routePortOwner, shellQuote(id), shellQuote(id))
}

func ownsRoutePort(id string) bool {
	data, err := os.ReadFile(routePortOwner)
	return err == nil && strings.TrimSpace(string(data)) == id
}

func releaseRoutePort(id string) string {
	return fmt.Sprintf("if [ -f %s ] && [ \"$(cat %s)\" = %s ]; then rm %s; fi\n", routePortOwner, routePortOwner, shellQuote(id), routePortOwner)
}
