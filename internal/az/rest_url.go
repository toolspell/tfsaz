package az

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strings"
)

var (
	errRestURL       = errors.New("--url requires a configured HTTPS collection URL or a collection-relative API path")
	errRestPath      = errors.New("--url must identify an Azure DevOps collection REST endpoint")
	errRestAmbiguous = errors.New("--url overlaps configured collection paths; use an unambiguous collection alias")
)

func (cfg config) restTarget(ctx context.Context, cwd, raw string) (*Profile, []string, url.Values, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || strings.Contains(raw, "#") ||
		(u.IsAbs() && (u.Scheme != "https" || u.Hostname() == "")) ||
		(!u.IsAbs() && strings.HasPrefix(raw, "//")) {
		return nil, nil, nil, errRestURL
	}
	parts, ok := pathSegments(u)
	if !ok {
		return nil, nil, nil, errRestPath
	}
	for _, part := range parts {
		if !collectionAPIRoot(part) && !validCatalogRouteValue(part) {
			return nil, nil, nil, errRestPath
		}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, nil, nil, errors.New("--url contains an invalid query string")
	}
	var profile *Profile
	if u.IsAbs() {
		profile, parts, err = cfg.absoluteRestPath(u, parts)
		if err != nil {
			return nil, nil, nil, err
		}
	} else {
		if !isRestPath(parts) {
			return nil, nil, nil, errRestPath
		}
		profile, _ = cfg.checkout(ctx, cwd)
		if profile == nil {
			return nil, nil, nil, errors.New("checkout does not identify a configured collection; use an absolute --url")
		}
	}
	if err := cfg.checkRestBoundary(profile, parts); err != nil {
		return nil, nil, nil, err
	}
	return profile, parts, query, nil
}

func (cfg config) absoluteRestPath(u *url.URL, parts []string) (*Profile, []string, error) {
	var profile *Profile
	var offset, longestBase int
	var matchedBase bool
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		for _, selector := range p.selectors() {
			base, ok := baseURL(selector)
			if !ok || !sameRESTServer(base, u) {
				continue
			}
			baseParts, _ := pathSegments(base)
			if !restPathHasPrefix(parts, baseParts) {
				continue
			}
			n := len(baseParts)
			matchedBase = true
			longestBase = max(longestBase, n)
			if n == 0 && legacyHost.MatchString(base.Host) && len(parts) > 0 && strings.EqualFold(parts[0], "DefaultCollection") {
				n++
			}
			if !isRestPath(parts[n:]) {
				continue
			}
			if profile != nil && (profile != p || offset != n) {
				return nil, nil, errRestAmbiguous
			}
			profile, offset = p, n
		}
	}
	if profile != nil && offset >= longestBase {
		return profile, parts[offset:], nil
	}
	if matchedBase {
		return nil, nil, errRestPath
	}
	return nil, nil, errors.New("--url does not match a configured collection")
}

// Azure DevOps advertises REST, Analytics and package protocol routes under
// these collection-relative roots.
func collectionAPIRoot(part string) bool {
	switch strings.ToLower(part) {
	case "_apis", "_odata", "_packaging":
		return true
	default:
		return false
	}
}

func isRestPath(parts []string) bool {
	for _, part := range parts[:min(len(parts), 3)] {
		if collectionAPIRoot(part) {
			return true
		}
	}
	return false
}

func restPathHasPrefix(path, prefix []string) bool {
	return len(path) >= len(prefix) && slices.EqualFunc(path[:len(prefix)], prefix, strings.EqualFold)
}

// A parent alias or constructed path could otherwise select a parent's PAT
// while the final HTTPS request targets a nested collection.
func (cfg config) checkRestBoundary(profile *Profile, path []string) error {
	base, ok := baseURL(profile.CollectionURL)
	if !ok {
		return errors.New("invalid HTTPS collection URL")
	}
	baseParts, _ := pathSegments(base)
	target := append(baseParts, path...)
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		if p == profile {
			continue
		}
		for _, selector := range p.selectors() {
			other, ok := baseURL(selector)
			if !ok || !sameRESTServer(base, other) {
				continue
			}
			otherParts, _ := pathSegments(other)
			if len(otherParts) >= len(baseParts) && len(otherParts) <= len(target) &&
				slices.EqualFunc(target[:len(otherParts)], otherParts, sameRESTBoundarySegment) {
				return errRestAmbiguous
			}
		}
	}
	return nil
}

func sameRESTBoundarySegment(a, b string) bool {
	// Compare after decoding again so an intermediary cannot turn a path such
	// as Child%2520Collection into another profile's Child%20Collection.
	decode := func(s string) string {
		for {
			next, err := url.PathUnescape(s)
			if err != nil || next == s {
				name, _, _ := strings.Cut(s, ";")
				return strings.TrimRight(strings.TrimSpace(name), ". ")
			}
			s = next
		}
	}
	return strings.EqualFold(a, b) || strings.EqualFold(decode(a), decode(b))
}

func sameRESTServer(a, b *url.URL) bool {
	// Equivalent HTTPS authorities must not bypass the nested-collection check.
	host := func(u *url.URL) string {
		name := strings.TrimSuffix(u.Hostname(), ".")
		if addr, err := netip.ParseAddr(name); err == nil {
			return addr.Unmap().String()
		}
		return name
	}
	port := func(u *url.URL) string {
		p := u.Port()
		if p == "" {
			return "443"
		}
		return strings.TrimLeft(p, "0")
	}
	return strings.EqualFold(host(a), host(b)) && port(a) == port(b)
}
