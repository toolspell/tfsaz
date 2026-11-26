package az

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func (a app) runProfiles(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "init" {
		return a.addProfile(ctx, args[1:], true)
	}
	if len(args) < 2 {
		return usageError{errors.New("expected profile add, list, logout, or remove")}
	}
	switch args[1] {
	case "add":
		return a.addProfile(ctx, args[2:], false)
	case "list":
		if len(args) != 2 {
			return usageError{errors.New("profile list takes no options")}
		}
		return a.listProfiles()
	case "logout":
		opts, err := parseProfileOptions(args[2:], "profile")
		if err != nil {
			return usageError{err}
		}
		if !profileID.MatchString(opts["profile"]) {
			return usageError{errors.New("profile logout requires --profile with a valid ID")}
		}
		return a.logoutProfile(opts["profile"])
	case "remove":
		opts, err := parseProfileOptions(args[2:], "profile", "collection", "service")
		if err != nil {
			return usageError{err}
		}
		id := opts["profile"]
		if !profileID.MatchString(id) {
			return usageError{errors.New("profile remove requires --profile with a valid ID")}
		}
		switch {
		case opts["collection"] != "" && opts["service"] != "":
			return usageError{errors.New("profile remove takes --collection or --service, not both")}
		case opts["collection"] != "":
			collection, err := collectionURL(opts["collection"])
			if err != nil {
				return usageError{err}
			}
			p := Profile{ID: id, CollectionURL: collection.String(), CredentialVersion: collectionCredentialVersion}
			return a.removeKeyringEntry(profileEntry(p))
		case opts["service"] != "":
			if !credentialServiceName.MatchString(opts["service"]) {
				return usageError{errors.New("--service requires a service that tfsaz profile list shows")}
			}
			return a.removeKeyringEntry(keyringEntry{opts["service"], id})
		}
		return a.removeProfile(id)
	default:
		return usageError{errors.New("expected profile add, list, logout, or remove")}
	}
}

func parseProfileOptions(args []string, allowed ...string) (map[string]string, error) {
	opts := make(map[string]string)
	for len(args) > 0 {
		name, value, joined := strings.Cut(args[0], "=")
		args = args[1:]
		name, long := strings.CutPrefix(name, "--")
		if !long || !slices.Contains(allowed, name) {
			return nil, errors.New("unsupported option or unexpected argument")
		}
		if _, duplicate := opts[name]; duplicate {
			return nil, fmt.Errorf("duplicate --%s", name)
		}
		if name == "no-login" {
			if joined {
				return nil, errors.New("--no-login takes no value")
			}
			value = "true"
		} else if !joined && len(args) > 0 && !strings.HasPrefix(args[0], "--") {
			value, args = args[0], args[1:]
		}
		if value == "" {
			return nil, fmt.Errorf("--%s requires a value", name)
		}
		opts[name] = value
	}
	return opts, nil
}

func collectionURL(raw string) (*url.URL, error) {
	collection, ok := baseURL(raw)
	if !ok || collection.Path == "" || strings.HasSuffix(collection.Host, ":") ||
		(strings.Contains(collection.Hostname(), ":") && !strings.HasPrefix(collection.Host, "[")) {
		return nil, errors.New("--collection requires a full HTTPS collection URL without credentials, query, or fragment")
	}
	if strings.HasPrefix(collection.Host, "[") {
		if address, err := netip.ParseAddr(collection.Hostname()); err != nil || !address.Is6() {
			return nil, errors.New("--collection has an invalid IPv6 address")
		}
	}
	if port := collection.Port(); port != "" {
		if number, err := strconv.ParseUint(port, 10, 16); err != nil || number == 0 {
			return nil, errors.New("--collection has an invalid port")
		}
	}
	return collection, nil
}

func (a app) newProfile(opts map[string]string, interactive bool) (Profile, error) {
	rawURL := opts["collection"]
	if rawURL == "" && interactive && a.stdinIsTerminal() {
		// The prompt is a courtesy; the read below reports a broken terminal.
		_, _ = fmt.Fprint(a.stderr, "Azure DevOps Server collection URL: ")
		line, err := bufio.NewReaderSize(a.stdin, 8192).ReadSlice('\n')
		if err != nil {
			return Profile{}, errors.New("could not read the collection URL")
		}
		rawURL = strings.TrimSpace(string(line))
	}
	collection, err := collectionURL(rawURL)
	if err != nil {
		return Profile{}, err
	}
	// A deterministic alias keeps repeated setup stable and gives each collection
	// its own host for T3's viewer lookups.
	sum := sha256.Sum256([]byte(collection.String()))
	alias := fmt.Sprintf("tfsaz-%x", sum[:12])
	p := Profile{
		ID:                opts["id"],
		Organization:      opts["organization"],
		CollectionURL:     collection.String(),
		CredentialVersion: collectionCredentialVersion,
	}
	if p.ID == "" {
		p.ID = alias
	}
	if !profileID.MatchString(p.ID) {
		return Profile{}, errors.New("--id requires a valid profile ID")
	}
	if p.Organization == "" {
		p.Organization = "https://" + alias + ".visualstudio.com"
	}
	organization, ok := baseURL(p.Organization)
	if !ok || !azureOrganization(organization) {
		return Profile{}, errors.New("--organization requires a recognized Azure-style HTTPS organization URL")
	}
	p.Organization = organization.String()
	if ca := opts["ca-file"]; ca != "" {
		if !filepath.IsAbs(ca) && a.cwd != "" {
			ca = filepath.Join(a.cwd, ca)
		}
		absolute, err := filepath.Abs(ca)
		if err != nil {
			return Profile{}, errors.New("could not resolve the collection CA file path")
		}
		p.CAFile = absolute
	}
	return p, nil
}

func sameProfileSelector(a, b string) bool {
	if canonical := canonicalAzureOrganization(a); canonical != "" {
		a = canonical
	}
	if canonical := canonicalAzureOrganization(b); canonical != "" {
		b = canonical
	}
	return strings.EqualFold(a, b)
}

func matchProfile(cfg config, requested Profile) (Profile, bool, error) {
	for _, p := range cfg.Profiles {
		if strings.EqualFold(p.ID, requested.ID) {
			if p.ID == requested.ID && p.CollectionURL == requested.CollectionURL &&
				p.Organization == requested.Organization && sameSetupPath(p.CAFile, requested.CAFile) {
				return p, true, nil
			}
			return Profile{}, false, fmt.Errorf("profile ID %s is already configured with different settings", p.ID)
		}
		for _, selector := range p.selectors() {
			if sameProfileSelector(selector, requested.Organization) || sameProfileSelector(selector, requested.CollectionURL) {
				return Profile{}, false, fmt.Errorf("organization or collection URL is already configured for profile %s", p.ID)
			}
		}
	}
	return requested, false, nil
}

func (a app) addProfile(ctx context.Context, args []string, interactive bool) error {
	opts, err := parseProfileOptions(args, "collection", "id", "organization", "ca-file", "no-login")
	if err != nil {
		return usageError{err}
	}
	requested, err := a.newProfile(opts, interactive)
	if err != nil {
		return usageError{err}
	}
	return withProfileConfigLock(true, func(path, target string) error {
		original, err := readConfigFile(target)
		fileExists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		cfg, err := validateConfig(original, path)
		if err != nil {
			return err
		}
		p, exists, err := matchProfile(cfg, requested)
		if err != nil {
			return err
		}
		if !exists {
			original.Profiles = append(original.Profiles, p)
			cfg, err := validateConfig(original, path)
			if err != nil {
				return err
			}
			records, err := readSetupRecords()
			if err != nil {
				return err
			}
			if err := cfg.addSetupAliases(records); err != nil {
				return err
			}
		}
		var pat string
		if opts["no-login"] == "" {
			if !exists {
				// A credential slot can outlive its config or be shared by
				// another config with the same profile ID and collection.
				_, err = a.profileCredential(p)
				switch {
				case err == nil:
					return fmt.Errorf("profile %s already has a credential in the OS keyring; use --no-login or choose another profile ID", p.ID)
				case !errors.Is(err, errNoCredential):
					return fmt.Errorf("%w for profile %s", errStoreUnavailable, p.ID)
				}
			}
			pat, err = a.verifyLogin(ctx, p)
			if err != nil {
				return err
			}
		}
		if !exists {
			// Publish the binding before writing its secret, so an interrupted
			// login can retry the same profile without leaving an orphaned PAT.
			if err := saveConfigFile(target, original, fileExists); err != nil {
				return err
			}
		}
		if pat != "" {
			if err := a.saveLogin(path, target, p, pat); err != nil {
				if !exists {
					return fmt.Errorf("%w; profile configuration was saved and login can be retried", err)
				}
				return err
			}
		}
		_, err = fmt.Fprintf(a.stdout, "Configured %s\t%s\t%s\n", p.ID, p.Organization, p.CollectionURL)
		return err
	})
}

func withProfileConfigLock(create bool, run func(path, target string) error) (err error) {
	path, err := configPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create TFSaz configuration directory: %w", err)
		}
	}
	target, err := filepath.EvalSymlinks(path)
	if create && errors.Is(err, os.ErrNotExist) {
		// A missing config is normal on first use. Resolve its parent so
		// alternate spellings of the directory all acquire the same lock.
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("could not resolve the TFSaz configuration file")
		}
		dir, err = filepath.EvalSymlinks(dir)
		target = filepath.Join(dir, filepath.Base(path))
	}
	if err != nil {
		return fmt.Errorf("resolve TFSaz configuration: %w", err)
	}
	if _, err := setupFileExists(target); err != nil {
		return err
	}
	release, err := acquireLockFile(target+".lock", errors.New("another profile transaction holds the TFSaz configuration lock; retry"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	return run(path, target)
}

func saveConfigFile(target string, cfg config, replace bool) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := publishSetupFile(target, append(data, '\n'), replace); err != nil {
		return fmt.Errorf("save TFSaz configuration: %w", err)
	}
	return nil
}

func (a app) listProfiles() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := readConfigFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cfg, err = validateConfig(cfg, path)
	if err != nil {
		return err
	}
	recorded, err := readKeyringList(a.keyringList)
	if err != nil {
		return err
	}
	var b strings.Builder
	if len(cfg.Profiles) == 0 {
		fmt.Fprintln(&b, "No collection profiles configured")
	}
	used := make(map[keyringEntry]bool, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		used[profileEntry(p)] = true
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", p.ID, p.Organization, p.CollectionURL, a.credentialState(profileEntry(p)))
	}
	recorded = slices.DeleteFunc(recorded, func(e keyringEntry) bool { return used[e] })
	if len(recorded) > 0 {
		fmt.Fprintln(&b, "\nKeyring entries that no profile in this configuration uses:")
		for _, e := range recorded {
			fmt.Fprintf(&b, "%s\t%s\t%s\n", e.Account, e.Service, a.credentialState(e))
		}
		fmt.Fprintln(&b, "Another configuration may still use them. Remove one with tfsaz profile remove --profile ID --service SERVICE.")
	}
	_, err = io.WriteString(a.stdout, b.String())
	return err
}

// findProfile reads the configuration under its lock and returns the
// unvalidated file content for rewriting, with the index of profile id.
func findProfile(path, target, id string, missing error) (config, Profile, int, error) {
	original, err := readConfigFile(target)
	if err != nil {
		return config{}, Profile{}, -1, err
	}
	cfg, err := validateConfig(original, path)
	if err != nil {
		return config{}, Profile{}, -1, err
	}
	i := slices.IndexFunc(cfg.Profiles, func(p Profile) bool { return p.ID == id })
	if i < 0 {
		return config{}, Profile{}, -1, missing
	}
	return original, cfg.Profiles[i], i, nil
}

func (a app) logoutProfile(id string) error {
	return withProfileConfigLock(false, func(path, target string) error {
		original, p, i, err := findProfile(path, target, id, errors.New("profile logout requires a configured profile ID"))
		if err != nil {
			return err
		}
		kept, err := a.releaseCredential(p)
		if err != nil {
			return err
		}
		// A legacy profile logs out by moving off the shared entry.
		if original.Profiles[i].CredentialVersion != collectionCredentialVersion {
			original.Profiles[i].CredentialVersion = collectionCredentialVersion
			if err := saveConfigFile(target, original, true); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(a.stdout, "Logged out %s\n", p.ID); err != nil {
			return err
		}
		return a.legacyEntryNote(kept, p.ID)
	})
}

func (a app) removeProfile(id string) error {
	return withProfileConfigLock(false, func(path, target string) (err error) {
		original, p, i, err := findProfile(path, target, id,
			errors.New("profile remove requires a configured profile ID; add --collection or --service to remove a keyring entry that no profile uses"))
		if err != nil {
			return err
		}
		// Hold the registry through the configuration update, so a concurrent
		// repo setup cannot register a checkout for the profile in between.
		registry, err := setupRegistryPath()
		if err != nil {
			return err
		}
		release, err := acquireSetupLock(registry + ".lock")
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, release()) }()
		records, err := readSetupRegistry(registry)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(records, func(r setupRecord) bool { return r.ProfileID == p.ID }) {
			return fmt.Errorf("profile %s has managed checkouts; run tfsaz repo restore for each of them in tfsaz repo list first", p.ID)
		}
		// Delete the PAT before the profile, so a retry after a failed
		// configuration write still finds the profile.
		kept, err := a.releaseCredential(p)
		if err != nil {
			return err
		}
		original.Profiles = slices.Delete(original.Profiles, i, i+1)
		if err := saveConfigFile(target, original, true); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "Removed profile %s\n", p.ID); err != nil {
			return err
		}
		return a.legacyEntryNote(kept, p.ID)
	})
}

// removeKeyringEntry deletes an entry that no profile in this configuration
// uses, including one written before TFSaz recorded its entries.
func (a app) removeKeyringEntry(entry keyringEntry) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	var found bool
	err = a.withKeyringList(func(list *keyringList) error {
		// The list lock orders this check against a concurrent login.
		cfg, err := readConfigFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if cfg, err = validateConfig(cfg, path); err != nil {
			return err
		}
		for _, p := range cfg.Profiles {
			if profileEntry(p) == entry {
				return fmt.Errorf("profile %s uses this keyring entry; run tfsaz profile logout or tfsaz profile remove --profile %s", p.ID, p.ID)
			}
		}
		found, err = a.deleteCredential(list, entry)
		return err
	})
	if err != nil {
		return err
	}
	if !found {
		_, err = fmt.Fprintf(a.stdout, "No keyring entry %s for %s\n", entry.Service, entry.Account)
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "Removed keyring entry %s for %s\n", entry.Service, entry.Account)
	return err
}
