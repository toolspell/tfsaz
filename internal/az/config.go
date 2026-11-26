package az

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

type Profile struct {
	ID                string   `json:"id"`
	Organization      string   `json:"organization"`
	CollectionURL     string   `json:"collectionUrl"`
	Aliases           []string `json:"aliases,omitempty"`
	CAFile            string   `json:"caFile,omitempty"`
	CredentialVersion int      `json:"credentialVersion,omitempty"`
}

type config struct {
	Profiles []Profile `json:"profiles"`
}

var profileID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)
var legacyHost = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.visualstudio\.com$`)

func validSegment(s string) bool {
	// Reject hidden separators or dot segments if an HTTP intermediary decodes again.
	for {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\") || strings.ContainsFunc(s, unicode.IsControl) {
			return false
		}
		decoded, err := url.PathUnescape(s)
		if err != nil || decoded == s {
			return true
		}
		s = decoded
	}
}

func pathSegments(u *url.URL) ([]string, bool) {
	path := strings.TrimSuffix(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if path == "" {
		return nil, true
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		s, err := url.PathUnescape(p)
		if err != nil || !validSegment(s) {
			return nil, false
		}
		parts[i] = s
	}
	return parts, true
}

// baseURL canonicalizes an exact collection selector. No credentials come from a URL.
func baseURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return nil, false
	}
	parts, ok := pathSegments(u)
	if !ok {
		return nil, false
	}
	u.Host = strings.ToLower(u.Host)
	u.Path, u.RawPath = "", ""
	if len(parts) > 0 {
		u.Path = "/" + strings.Join(parts, "/")
	}
	return u, true
}

func azureOrganization(u *url.URL) bool {
	return (u.Host == "dev.azure.com" && strings.Count(u.Path, "/") == 1) || (legacyHost.MatchString(u.Host) && u.Path == "")
}

func (p Profile) selectors() []string {
	return append([]string{p.Organization, p.CollectionURL}, p.Aliases...)
}

func configPath() (string, error) {
	if path := os.Getenv("TFSAZ_CONFIG"); path != "" {
		if !filepath.IsAbs(path) {
			return "", errors.New("TFSAZ_CONFIG must be an absolute path")
		}
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tfsaz", "config.json"), nil
}

func loadConfig() (config, error) {
	path, err := configPath()
	if err != nil {
		return config{}, err
	}
	return loadConfigAt(path)
}

func loadConfigAt(path string) (config, error) {
	cfg, err := readConfigFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config{}, fmt.Errorf("%w; run tfsaz init to configure a collection", err)
	}
	if err != nil {
		return config{}, err
	}
	if len(cfg.Profiles) == 0 {
		return config{}, errors.New("no collection profiles configured")
	}
	return validateConfig(cfg, path)
}

func readConfigFile(path string) (config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, fmt.Errorf("read TFSaz configuration: %w", err)
	}
	var cfg *config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || cfg == nil || d.Decode(new(any)) != io.EOF {
		return config{}, errors.New("invalid TFSaz configuration; check JSON syntax and field names")
	}
	return *cfg, nil
}

func validateConfig(cfg config, path string) (config, error) {
	cfg.Profiles = append([]Profile(nil), cfg.Profiles...)
	ids, selectors := map[string]bool{}, map[string]bool{}
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		if !profileID.MatchString(p.ID) || ids[strings.ToLower(p.ID)] {
			return config{}, fmt.Errorf("invalid or duplicate ID in profile %d", i+1)
		}
		ids[strings.ToLower(p.ID)] = true
		if p.CredentialVersion != 0 && p.CredentialVersion != collectionCredentialVersion {
			return config{}, fmt.Errorf("unsupported credential version in profile %s", p.ID)
		}
		values := p.selectors()
		for j, raw := range values {
			u, ok := baseURL(raw)
			if !ok || (j == 0 && !azureOrganization(u)) || (j == 1 && u.Path == "") {
				return config{}, fmt.Errorf("profile %s has an invalid organization or collection URL", p.ID)
			}
			values[j] = u.String()
			key := strings.ToLower(u.String())
			if selectors[key] {
				return config{}, fmt.Errorf("duplicate organization or collection URL in profile %s", p.ID)
			}
			selectors[key] = true
		}
		p.Organization, p.CollectionURL, p.Aliases = values[0], values[1], values[2:]
		if p.CAFile != "" && !filepath.IsAbs(p.CAFile) {
			p.CAFile = filepath.Join(filepath.Dir(path), p.CAFile)
		}
	}
	return cfg, nil
}
