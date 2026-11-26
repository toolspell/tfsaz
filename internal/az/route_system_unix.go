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
	prompt := "TFSaz needs administrator rights to change the hosts file and loopback port 443. Password for %u: "
	_, err = runRouteSystemCommand(ctx, nil, sudo, "-p", prompt, "--", "/bin/sh", "-c", "set -eu\numask 022\nPATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\n"+script)
	if err != nil {
		return fmt.Errorf("administrator step: %w (sudo needs an interactive terminal or cached credentials)", err)
	}
	return nil
}

// Stage the complete replacement alongside the system hosts file and preserve
// its metadata. Prepending our lines preserves the original final newline.
func routeHostsShell(path, id string, aliases []string, install bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "h=%s; m=%s\n", shellQuote(path), shellQuote(routeHostsMarker(id)))
	b.WriteString(`[ -f "$h" ] && [ ! -L "$h" ] || exit 1
t=$(mktemp "$h.tfsaz.XXXXXX")
trap 'rm -f "$t"' EXIT
orig=$(mktemp "$h.tfsaz.XXXXXX")
trap 'rm -f "$t" "$orig"' EXIT
cp -p "$h" "$orig"
cp -p "$orig" "$t"
`)
	perl := `use strict; use warnings; binmode STDOUT; my ($m, $install, $h, @lines) = @ARGV;
open(my $in, "<:raw", $h) or die "cannot read hosts";
my $text = do { local $/; <$in> } // "";
my %aliases = map { (split / /)[1] => 1 } @lines;
if ($install) { my $nl = index($text, "\r\n") >= 0 ? "\r\n" : "\n"; print $_, $nl for @lines; }
while ($text =~ /([^\n]*\n|[^\n]+\z)/g) {
  my $line = $1;
  next if $line =~ /\Q$m\E(?:\r?\n)?\z/;
  if ($install) {
    my $names = $line; $names =~ s/#.*//; my @names = split ' ', $names; shift @names;
    for (@names) { s/\.$//; die "foreign hosts entry already names this alias" if $aliases{lc $_}; }
  }
  print $line;
}
close STDOUT or die "cannot stage hosts";
`
	flag := "0"
	if install {
		flag = "1"
	}
	fmt.Fprintf(&b, "/usr/bin/perl -e %s -- \"$m\" %s \"$orig\"", shellQuote(perl), flag)
	for _, line := range routeHostsLines(id, aliases) {
		b.WriteString(" " + shellQuote(line))
	}
	b.WriteString(` > "$t"
cmp -s "$orig" "$h" || { echo "hosts changed during setup; retry" >&2; exit 1; }
cmp -s "$t" "$h" || mv -f "$t" "$h"
rm -f "$t" "$orig"; trap - EXIT
`)
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
