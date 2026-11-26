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
// elevated script implements the same algorithm. Linux runs only the shell
// fragment routeHostsShell, whose tests check it against this function, so
// deadcode lists it as unreachable there.
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
				return fmt.Errorf("the hosts file already has an entry for %s that TFSaz did not add; remove it first", name)
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
