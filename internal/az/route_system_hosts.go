package az

import (
	"fmt"
	"os"
	"strings"
)

// Hosts entries owned by one configuration end with this exact marker. Lines
// without it are never changed, removed or reordered.
func routeHostsMarker(id string) string { return " # tfsaz-route " + id }

func routeHostsLines(id string, aliases []string) []string {
	var lines []string
	for _, a := range aliases {
		lines = append(lines, "127.0.0.1 "+a+routeHostsMarker(id), "::1 "+a+routeHostsMarker(id))
	}
	return lines
}

// editRouteHosts drops this configuration's marked lines and, on install,
// appends fresh ones. It keeps the file's line ending style. The Windows
// elevated script implements the same algorithm.
func editRouteHosts(content, id string, aliases []string, install bool) string {
	var result strings.Builder
	if install {
		nl := "\n"
		if strings.Contains(content, "\r\n") || content == "" && routeHostsCRLF {
			nl = "\r\n"
		}
		for _, line := range routeHostsLines(id, aliases) {
			result.WriteString(line + nl)
		}
	}
	marker := routeHostsMarker(id)
	for line := range strings.Lines(content) {
		if !strings.HasSuffix(strings.TrimRight(line, "\r\n"), marker) {
			result.WriteString(line)
		}
	}
	return result.String()
}

func routeHostsHasMarker(content, id string) bool {
	marker := routeHostsMarker(id)
	for line := range strings.Lines(content) {
		if strings.HasSuffix(strings.TrimRight(line, "\r\n"), marker) {
			return true
		}
	}
	return false
}

// routeHostsConflict reports an unmarked or foreign-marked entry that already
// names one of the aliases. Resolvers use the first match, so tfsaz refuses
// rather than rely on order or edit someone else's line.
func routeHostsConflict(content, id string, aliases []string) error {
	want := make(map[string]bool, len(aliases))
	for _, a := range aliases {
		want[a] = true
	}
	marker := routeHostsMarker(id)
	for line := range strings.Lines(content) {
		line = strings.TrimRight(line, "\r\n")
		if strings.HasSuffix(line, marker) {
			continue
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		for _, name := range fields[min(1, len(fields)):] {
			if want[strings.ToLower(strings.TrimSuffix(name, "."))] {
				return fmt.Errorf("the hosts file already has an entry for %s that tfsaz did not add; remove it first", name)
			}
		}
	}
	return nil
}

func readRouteHosts() (string, error) {
	data, err := os.ReadFile(routeHostsPath())
	if err != nil {
		return "", fmt.Errorf("read the hosts file: %w", err)
	}
	return string(data), nil
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
