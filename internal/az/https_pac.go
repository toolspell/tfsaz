package az

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

var pacHostLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func writeRoutePAC(out io.Writer, hosts []string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("PAC port must be between 1 and 65535")
	}
	var names []string
	seen := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		host = strings.TrimSuffix(host, ".")
		if len(host) > 253 {
			return errors.New("invalid PAC hostname")
		}
		for _, label := range strings.Split(host, ".") {
			if !pacHostLabel.MatchString(label) {
				return errors.New("invalid PAC hostname")
			}
		}
		host = strings.ToLower(host)
		if !seen[host] {
			names = append(names, host)
			seen[host] = true
		}
	}
	slices.Sort(names)
	var script strings.Builder
	script.WriteString("function FindProxyForURL(url, host) {\n")
	if len(names) > 0 {
		script.WriteString("  switch (host.toLowerCase().replace(/\\.$/, \"\")) {\n")
		for _, name := range names {
			fmt.Fprintf(&script, "    case %q:\n", name)
		}
		// WinHTTP ignores SOCKS5 entries. Give it the same local refusal via HTTP.
		fmt.Fprintf(&script, "      return \"SOCKS5 127.0.0.1:%[1]d; PROXY 127.0.0.1:%[1]d\";\n  }\n", port)
	}
	script.WriteString("  return \"DIRECT\";\n}\n")
	_, err := io.WriteString(out, script.String())
	return err
}
