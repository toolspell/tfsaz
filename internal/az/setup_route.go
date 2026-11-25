package az

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// setupRecord binds the Git transport and T3 identity to one saved checkout.
// Only the configured Profile can construct a REST client or select credentials.
type setupRecord struct {
	Repo          string `json:"repo"`
	GitDir        string `json:"gitDir"`
	ProfileID     string `json:"profileId"`
	CollectionURL string `json:"collectionUrl"`
	Project       string `json:"project"`
	Repository    string `json:"repository"`
	Host          string `json:"host"`
	OriginalURL   string `json:"originalUrl"`
	RemoteURL     string `json:"remoteUrl"`
	Transport     string `json:"transport,omitempty"`
	SSHURL        string `json:"sshUrl"`
	SSHConfig     string `json:"sshConfig"`
	SSHCommand    string `json:"sshCommand"`
	Phase         string `json:"phase"`
}

func preserveSetupRemote(scheme, host, project, repository string) string {
	if scheme == "ssh" {
		return (&url.URL{
			Scheme: "ssh",
			User:   url.User("git"),
			Host:   setupSyntheticSSHHost,
			Path:   "/v3/" + strings.TrimSuffix(host, ".visualstudio.com") + "/" + project + "/" + repository,
		}).String()
	}
	path := "/" + project + "/_git/" + repository
	if strings.EqualFold(project, "DefaultCollection") {
		path = "/DefaultCollection" + path
	}
	return (&url.URL{Scheme: "https", Host: host, Path: path}).String()
}

func preserveSetupTarget(raw string) (*url.URL, error) {
	u, err := nativeSetupURL(raw)
	if err != nil {
		return nil, err
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "dev.azure.com" || strings.HasSuffix(host, ".dev.azure.com") || strings.HasSuffix(host, ".visualstudio.com") {
		return nil, errors.New("expected a native Azure DevOps Server origin")
	}
	if u.Scheme == "ssh" && (!validSetupSSHHost(u.Hostname()) ||
		(u.Port() != "" && !setupSSHValidPort(u.Port())) ||
		(u.User != nil && !setupSSHUserName.MatchString(u.User.Username()))) {
		return nil, errors.New("unsupported native SSH host, port, or user")
	}
	return u, nil
}

func makePreserveSetupRecord(cfg config, c setupCheckout, original, profile string) (setupRecord, error) {
	target, err := preserveSetupTarget(original)
	if err != nil {
		return setupRecord{}, err
	}
	p, scope, err := setupScope(cfg, target, profile)
	if err != nil {
		return setupRecord{}, err
	}
	if strings.HasPrefix(scope.Project, "@") || strings.HasPrefix(scope.Repository, "@") {
		return setupRecord{}, errors.New("stock T3 cannot scope a repository or project name beginning with @")
	}
	host := setupHost(p.ID, p.CollectionURL, scope.Project, scope.Repository)
	if other, _ := cfg.selectBase("https://" + host); other != nil && other.ID != p.ID {
		return setupRecord{}, errors.New("generated organization conflicts with an existing profile")
	}
	return setupRecord{
		Repo: c.repo, GitDir: c.gitDir, ProfileID: p.ID, CollectionURL: p.CollectionURL,
		Project: scope.Project, Repository: scope.Repository, Host: host,
		OriginalURL: original, RemoteURL: preserveSetupRemote(target.Scheme, host, scope.Project, scope.Repository),
		Transport: target.Scheme,
	}, nil
}

func setupHost(profileID, collectionURL, project, repository string) string {
	key := strings.ToLower(strings.Join([]string{profileID, collectionURL, project, repository}, "\x00"))
	sum := sha256.Sum256([]byte(key))
	return "tfsaz-" + hex.EncodeToString(sum[:12]) + ".visualstudio.com"
}

// addSetupAliases builds a routing table in memory. Changing a profile's
// endpoint invalidates its old bindings even if its stable ID was reused.
func (cfg config) addSetupAliases(records []setupRecord) error {
	for _, r := range records {
		if !validSegment(r.Project) || !validSegment(r.Repository) || r.Host != setupHost(r.ProfileID, r.CollectionURL, r.Project, r.Repository) {
			return errors.New("invalid managed repository binding")
		}
		org := "https://" + r.Host
		existing, _ := cfg.selectBase(org)
		if existing != nil {
			if existing.ID != r.ProfileID || existing.CollectionURL != r.CollectionURL {
				return errors.New("managed repository alias conflicts with a configured profile")
			}
			continue
		}
		for i := range cfg.Profiles {
			p := &cfg.Profiles[i]
			if p.ID != r.ProfileID || p.CollectionURL != r.CollectionURL {
				continue
			}
			p.Aliases = append(p.Aliases, org)
			break
		}
	}
	return nil
}

func nativeSetupURL(raw string) (*url.URL, error) {
	invalid := errors.New("expected an HTTPS or SSH repository URL without embedded credentials or query parameters")
	if raw == "" || strings.ContainsFunc(raw, unicode.IsControl) || strings.ContainsAny(raw, "?#") {
		return nil, invalid
	}
	if !strings.Contains(raw, "://") {
		// SCP spelling with an absolute server path is equivalent to ssh://.
		authority, path, ok := strings.Cut(raw, ":/")
		if !ok || strings.Contains(authority, "/") {
			return nil, invalid
		}
		// Git sends SCP-style paths literally, including percent signs.
		raw = "ssh://" + authority + (&url.URL{Path: "/" + path}).EscapedPath()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "ssh") {
		return nil, invalid
	}
	if u.User != nil {
		if _, password := u.User.Password(); password || u.Scheme != "ssh" {
			return nil, invalid
		}
	}
	parts, ok := pathSegments(u)
	if !ok || len(parts) < 3 || parts[len(parts)-2] != "_git" {
		return nil, errors.New("expected a native Azure DevOps Server _git repository path")
	}
	return u, nil
}

// SSH often uses a different port from REST. Match the complete collection
// path and hostname, then require explicit selection for ambiguous listeners.
func setupScope(cfg config, u *url.URL, profileID string) (*Profile, Scope, error) {
	if profileID != "" {
		for i := range cfg.Profiles {
			if cfg.Profiles[i].ID == profileID {
				cfg.Profiles = cfg.Profiles[i : i+1]
				break
			}
		}
	}
	if u.Scheme == "https" {
		p, s := cfg.remoteScope(u.String())
		if p != nil && (profileID == "" || p.ID == profileID) {
			return p, s, nil
		}
		return nil, Scope{}, errors.New("HTTPS origin does not match the requested collection profile")
	}
	var selected *Profile
	var scope Scope
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		if profileID != "" && p.ID != profileID {
			continue
		}
		for _, selector := range append([]string{p.CollectionURL}, p.Aliases...) {
			base, _ := baseURL(selector)
			if base == nil || base.Path == "" || !strings.EqualFold(base.Hostname(), u.Hostname()) {
				continue
			}
			candidate := url.URL{Scheme: "https", Host: base.Host, Path: u.Path, RawPath: u.RawPath}
			profileConfig := config{Profiles: cfg.Profiles[i : i+1]}
			matched, s := profileConfig.remoteScope(candidate.String())
			if matched != p {
				continue
			}
			if selected != nil && selected != p {
				return nil, Scope{}, errors.New("SSH origin matches multiple collection profiles; specify --profile ID")
			}
			selected, scope = p, s
		}
	}
	if selected == nil {
		return nil, Scope{}, errors.New("SSH origin does not match a configured collection hostname and path")
	}
	return selected, scope, nil
}

func makeSetupRecord(ctx context.Context, cfg config, repo, gitDir, originalURL, overrideSSH, profileID, sshConfigPath string) (setupRecord, error) {
	var r setupRecord
	original, err := nativeSetupURL(originalURL)
	if err != nil {
		return r, err
	}
	p, scope, err := setupScope(cfg, original, profileID)
	if err != nil {
		return r, err
	}
	sshURL := overrideSSH
	if sshURL == "" {
		if original.Scheme == "ssh" {
			sshURL = original.String()
		} else {
			client, err := (app{store: systemStore{}}).client(*p)
			if err != nil {
				return r, fmt.Errorf("resolve the SSH clone URL (or supply --ssh-url): %w", err)
			}
			ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			var result struct{ SSHURL string }
			err = client.Get(ctx, []string{scope.Project, "_apis", "git", "repositories", scope.Repository}, url.Values{"api-version": {gitReadVersion}}, &result)
			if err != nil {
				return r, fmt.Errorf("read repository SSH URL: %w", err)
			}
			sshURL = result.SSHURL
		}
	}
	ssh, err := nativeSetupURL(sshURL)
	if err != nil || ssh.Scheme != "ssh" {
		return r, errors.New("expected a native SSH clone URL for the repository")
	}
	_, sshScope, err := setupScope(cfg, ssh, p.ID)
	if err != nil || !strings.EqualFold(sshScope.Project, scope.Project) || !strings.EqualFold(sshScope.Repository, scope.Repository) {
		return r, errors.New("SSH clone URL must identify the same configured collection, project and repository as origin")
	}
	if strings.HasPrefix(scope.Project, "@") || strings.HasPrefix(scope.Repository, "@") {
		return r, errors.New("stock T3 cannot scope a repository or project name beginning with @")
	}
	host := setupHost(p.ID, p.CollectionURL, scope.Project, scope.Repository)
	org := strings.TrimSuffix(host, ".visualstudio.com")
	remote := url.URL{
		Scheme: "ssh",
		User:   url.User("git"),
		Host:   setupSyntheticSSHHost,
		Path:   "/v3/" + org + "/" + scope.Project + "/" + scope.Repository,
	}
	r = setupRecord{
		Repo:          repo,
		GitDir:        gitDir,
		ProfileID:     p.ID,
		CollectionURL: p.CollectionURL,
		Project:       scope.Project,
		Repository:    scope.Repository,
		Host:          host,
		OriginalURL:   originalURL,
		RemoteURL:     remote.String(),
		SSHURL:        ssh.String(),
		SSHConfig:     sshConfigPath,
	}
	if other, _ := cfg.selectBase("https://" + host); other != nil && other.ID != p.ID {
		return setupRecord{}, errors.New("generated organization conflicts with an existing profile")
	}
	r.SSHCommand, err = setupSSHCommand(r)
	return r, err
}
