package az

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

// browserTarget also handles ordinary web links when the OS dispatches them to
// tfsaz as the default browser. They need no checkout, profile, or credential.
func browserTarget(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		strings.Contains(raw, "\\") || strings.ContainsFunc(raw, unicode.IsControl) || u.User != nil {
		return "", errors.New("expected an absolute HTTP or HTTPS web link without embedded credentials")
	}
	host := u.Hostname()
	if strings.ContainsFunc(host, func(r rune) bool { return r > unicode.MaxASCII }) {
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return "", errors.New("invalid international hostname in web link")
		}
	}
	host = strings.ToLower(strings.TrimRight(host, "."))
	if host != "dev.azure.com" && !legacyHost.MatchString(host) {
		return raw, nil
	}
	// Reject alternative spellings that browsers can canonicalize differently
	// from net/url before deciding an Azure organization is unrelated.
	if !strings.EqualFold(u.Hostname(), host) {
		return "", errors.New("use the canonical Azure hostname")
	}
	parts, ok := pathSegments(u)
	if !ok || (host == "dev.azure.com" && len(parts) > 0 && strings.Contains(parts[0], "%")) {
		return "", errors.New("invalid Azure web link path")
	}
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	records, err := readSetupRecords()
	if err != nil {
		return "", err
	}
	// Match HTTP inputs against the same ownership rules, then decline to
	// forward a registered HTTP alias. Real cloud organizations can pass.
	probe := *u
	probe.Scheme = "https"
	route, err := mapHTTPSRoute(cfg, records, probe.String())
	if errors.Is(err, errNoHTTPSRoute) {
		if _, managed := gitAzureAlias(probe.String()); managed {
			return "", errors.New("no active setup for this managed browser alias")
		}
		return raw, nil
	}
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" || route.Action != httpsActionRedirect {
		return "", errors.New("registered aliases require an HTTPS browser page")
	}
	return route.Destination, nil
}
