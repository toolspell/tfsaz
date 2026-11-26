package az

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// The published identity survives an interrupted OS operation. Restore uses it
// even if the configured repositories, private key or leaf validity have changed.
type routeIdentity struct {
	ConfigFile string   `json:"configFile"`
	Aliases    []string `json:"aliases"`
	SHA256     string   `json:"leafSHA256"`
}

const routeCertName = "alias.pem"
const routeKeyName = "alias-key.pem"
const routeIdentityName = "identity.json"

func stageRouteIdentity(dir, configFile string, aliases []string) (_ routeIdentity, err error) {
	identity := routeIdentity{ConfigFile: configFile, Aliases: aliases}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identity, err
	}
	now := time.Now()
	template := &x509.Certificate{
		Subject: pkix.Name{CommonName: aliases[0]}, DNSNames: aliases,
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(90 * 24 * time.Hour),
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return identity, err
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return identity, err
	}
	identity.SHA256 = fmt.Sprintf("%x", sha256.Sum256(der))
	info, err := json.Marshal(identity)
	if err != nil {
		return identity, err
	}
	staged, err := os.MkdirTemp(filepath.Dir(dir), ".tfsaz-route-")
	if err != nil {
		return identity, err
	}
	// The staged directory holds a copy of the private key until the rename.
	defer removeStagedTree(staged, &err)
	if err := protectRouteDirectory(staged); err != nil {
		return identity, fmt.Errorf("protect browser route key directory: %w", err)
	}
	for name, content := range map[string][]byte{
		routeCertName:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		routeKeyName:      pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}),
		routeIdentityName: info,
	} {
		if err := publishSetupFile(filepath.Join(staged, name), content, false); err != nil {
			return identity, err
		}
	}
	if err := os.Rename(staged, dir); err != nil {
		return identity, err
	}
	return identity, syncSetupDir(filepath.Dir(dir))
}

func readRouteIdentity(dir, configFile string) (routeIdentity, error) {
	var identity routeIdentity
	info, err := os.Lstat(dir)
	if err != nil {
		return identity, err
	}
	if !info.IsDir() {
		return identity, errors.New("managed browser route state must be a directory")
	}
	data, err := readSetupFile(filepath.Join(dir, routeIdentityName))
	if err != nil {
		return identity, fmt.Errorf("incomplete browser route identity at %s: %v", dir, err)
	}
	if json.Unmarshal(data, &identity) != nil || routeConfigKey(identity.ConfigFile) != routeConfigKey(configFile) || len(identity.Aliases) == 0 {
		return identity, errors.New("invalid browser route identity or configuration path")
	}
	for i, host := range identity.Aliases {
		if !generatedAliasHost.MatchString(host) || i > 0 && host <= identity.Aliases[i-1] {
			return identity, errors.New("invalid alias in stored browser route identity")
		}
	}
	digest, err := hex.DecodeString(identity.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return identity, errors.New("invalid managed certificate fingerprint")
	}
	data, err = readSetupFile(filepath.Join(dir, routeCertName))
	if err != nil {
		return identity, fmt.Errorf("read stored redirect certificate: %v", err)
	}
	block, remainder := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(remainder) != 0 || fmt.Sprintf("%x", sha256.Sum256(block.Bytes)) != identity.SHA256 {
		return identity, errors.New("stored redirect certificate differs from its recorded fingerprint")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !slices.Equal(leaf.DNSNames, identity.Aliases) || leaf.IsCA {
		return identity, errors.New("stored redirect certificate differs from its recorded aliases")
	}
	return identity, nil
}

var errRouteSystemUnsupported = errors.New("browser routing setup supports macOS, Linux and Windows only")

// routeSystemPlatform runs before setup or restore writes anything, so an
// unsupported system keeps no private key or recovery certificate.
var routeSystemPlatform = func() error {
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		return nil
	}
	return errRouteSystemUnsupported
}

type routeSystemOperation func(ctx context.Context, install bool, id, dir, configFile, certFile, keyFile, leafSHA256 string, aliases []string) error

func runManagedRoute(ctx context.Context, args []string, out io.Writer, system routeSystemOperation) (err error) {
	action := args[0]
	var rawURL, installation string
	switch {
	case action == "verify" && len(args) == 2:
		rawURL = args[1]
		if rawURL == "" || rawURL[0] == '-' {
			return usageError{errors.New("expected route verify [HTTPS_PAGE_URL]")}
		}
	case action == "restore" && len(args) > 1:
		if len(args) != 3 || args[1] != "--installation" || !routeInstallationID.MatchString(args[2]) {
			return usageError{errors.New("expected route restore [--installation ID] with an ID that route restore lists")}
		}
		installation = args[2]
	case len(args) != 1:
		return usageError{fmt.Errorf("unexpected arguments for route %s", action)}
	}
	if err := routeSystemPlatform(); err != nil {
		return err
	}
	file, err := configPath()
	if err != nil {
		return err
	}
	dir := filepath.Clean(file) + ".browser-route"
	if action == "verify" {
		if _, err := readRouteIdentity(dir, file); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errors.New("managed browser routing is absent; run tfsaz route setup")
			}
			return err
		}
		return verifyManagedRoute(ctx, file, filepath.Join(dir, routeCertName), rawURL, out)
	}
	// Keep the public recovery certificate and lock outside the configuration
	// tree so deleting a checkout or config directory cannot strand OS changes.
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	recovery := filepath.Join(home, ".tfsaz", "route-recovery")
	id := routeSystemID(file)
	if installation != "" && installation != id {
		// Another configuration's installation. Without a recorded path, only
		// its recovery certificate is known.
		if file, err = readRouteConfigRecord(recovery, installation); err != nil {
			return err
		}
		id, dir = installation, ""
		if file != "" {
			dir = filepath.Clean(file) + ".browser-route"
		}
	}
	receipt := filepath.Join(recovery, id+".pem")
	record := filepath.Join(recovery, id+routeConfigRecordSuffix)
	if action == "restore" && (dir == "" || !routeStateExists(dir)) && !routeStateExists(receipt) {
		return writeRouteNothingRecorded(out, recovery, installation)
	}
	// Runs after the unlock below: the last restore leaves no recovery directory.
	defer removeEmptyRouteRecovery(recovery)
	unlock, err := acquireRouteRecoveryLock(recovery, receipt+".lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	var identity routeIdentity
	var certFile, keyFile string
	missing := dir == ""
	if !missing {
		var readErr error
		identity, readErr = readRouteIdentity(dir, file)
		missing = errors.Is(readErr, os.ErrNotExist)
		if readErr != nil && !missing {
			return readErr
		}
		certFile, keyFile = filepath.Join(dir, routeCertName), filepath.Join(dir, routeKeyName)
	}
	if action == "restore" {
		if missing {
			leaf, err := readRouteRecoveryLeaf(receipt)
			if errors.Is(err, os.ErrNotExist) {
				return writeRouteNothingRecorded(out, recovery, installation)
			}
			if err != nil {
				return err
			}
			identity.SHA256, identity.Aliases = fingerprintHex(leaf.Raw), leaf.DNSNames
		}
		if err := system(ctx, false, id, dir, file, certFile, keyFile, identity.SHA256, identity.Aliases); err != nil {
			return fmt.Errorf("restore browser routing: %w; ownership information retained for retry", err)
		}
		// Atomically retire the identity before deleting its files. Never recurse
		// into the installation directory, which may contain user-created files.
		if !missing {
			if err := retireRouteIdentity(dir); err != nil {
				return err
			}
		}
		// The receipt goes last: while it exists, restore can still find the
		// installation.
		for _, path := range []string{record, receipt} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		message := "Restored browser routing."
		if dir == "" {
			message += " Its configuration path was not recorded, so a <config-file>.browser-route directory beside that configuration may remain; run tfsaz route restore with that configuration to remove it."
		}
		_, err = fmt.Fprintln(out, message)
		return err
	}

	cfg, err := loadConfigAt(file)
	if err != nil {
		return err
	}
	records, err := readSetupRegistry(setupRegistryForConfig(file))
	if err != nil {
		return err
	}
	allowed := redirectAliasHosts(cfg, records)
	aliases := slices.Sorted(maps.Keys(allowed))
	if len(aliases) == 0 {
		return errors.New("no registered dedicated browser aliases; run tfsaz repo setup in a checkout first")
	}
	if missing {
		if _, err := os.Lstat(receipt); err == nil {
			return errors.New("a previous browser route installation has a recovery record; run tfsaz route restore first")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		identity, err = stageRouteIdentity(dir, file, aliases)
		if err != nil {
			return err
		}
	}
	if !slices.Equal(aliases, identity.Aliases) {
		return errors.New("registered browser aliases have changed; run tfsaz route restore and then tfsaz route setup")
	}
	if _, _, err := loadRedirectCertificate(certFile, keyFile, allowed, time.Now()); err != nil {
		return fmt.Errorf("managed browser identity needs renewal; run tfsaz route restore and then setup: %w", err)
	}
	der, err := readRouteSystemLeaf(certFile)
	if err != nil {
		return err
	}
	if saved, err := readRouteSystemLeaf(receipt); errors.Is(err, os.ErrNotExist) {
		if err := publishSetupFile(receipt, routeSystemPEM(der), false); err != nil {
			return err
		}
	} else if err != nil || fingerprintHex(saved) != identity.SHA256 {
		return errors.New("browser route recovery certificate differs from the installation identity")
	}
	// Also records the path for installations that an older version set up.
	if err := writeRouteConfigRecord(record, file); err != nil {
		return err
	}
	if err := system(ctx, true, id, dir, file, certFile, keyFile, identity.SHA256, identity.Aliases); err != nil {
		return fmt.Errorf("configure browser routing: %w; rerun tfsaz route setup or tfsaz route restore", err)
	}
	_, err = fmt.Fprintln(out, "Browser routing configured. Run tfsaz route verify with a PR URL and open it in your intended browser.")
	return err
}

const routeNothingRecorded = "No installation recorded for this configuration. If you manually deleted both the route directory and the recovery certificate, inspect OS routing and trust before using the alias."

// routeConfigRecordSuffix names the file beside a recovery certificate that
// records the configuration path, because the installation ID is a one-way
// hash of it.
const routeConfigRecordSuffix = ".config.json"

type routeConfigRecord struct {
	ConfigFile string `json:"configFile"`
}

func writeRouteConfigRecord(path, configFile string) error {
	data, err := json.Marshal(routeConfigRecord{ConfigFile: configFile})
	if err != nil {
		return err
	}
	if saved, err := readSetupFile(path); err == nil && bytes.Equal(saved, data) {
		return nil
	}
	return publishSetupFile(path, data, true)
}

// readRouteConfigRecord returns the recorded configuration path of an
// installation, or "" when none was recorded.
func readRouteConfigRecord(recovery, id string) (string, error) {
	path := filepath.Join(recovery, id+routeConfigRecordSuffix)
	data, err := readSetupFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var record routeConfigRecord
	if json.Unmarshal(data, &record) != nil || !filepath.IsAbs(record.ConfigFile) || routeSystemID(record.ConfigFile) != id {
		return "", fmt.Errorf("invalid configuration record %s; delete it to restore from the recovery certificate alone", path)
	}
	return record.ConfigFile, nil
}

func readRouteRecoveryLeaf(receipt string) (*x509.Certificate, error) {
	der, err := readRouteSystemLeaf(receipt)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil || !leaf.BasicConstraintsValid || leaf.IsCA || len(leaf.DNSNames) == 0 {
		return nil, errors.New("invalid browser route recovery certificate")
	}
	names := make(map[string]bool, len(leaf.DNSNames))
	for _, alias := range leaf.DNSNames {
		names[alias] = generatedAliasHost.MatchString(alias)
	}
	// Expired installations must still be removable.
	if _, err := validateRedirectLeaf(leaf, names, leaf.NotBefore); err != nil {
		return nil, err
	}
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
		return nil, errors.New("the browser route recovery certificate must be self-signed")
	}
	return leaf, nil
}

func writeRouteNothingRecorded(out io.Writer, recovery, installation string) error {
	message := routeNothingRecorded
	if installation != "" {
		message = "No installation " + installation + " recorded."
	}
	if _, err := fmt.Fprintln(out, message); err != nil {
		return err
	}
	return listRouteInstallations(out, recovery)
}

// listRouteInstallations prints the installations recorded in the recovery
// directory. It only reads.
func listRouteInstallations(out io.Writer, recovery string) error {
	entries, err := os.ReadDir(recovery)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".pem")
		if !ok || !routeInstallationID.MatchString(id) {
			continue
		}
		configFile, err := readRouteConfigRecord(recovery, id)
		switch {
		case err != nil:
			configFile = err.Error()
		case configFile == "":
			configFile = "not recorded"
		}
		fmt.Fprintf(&b, "  %s\n    configuration: %s\n", id, configFile)
		leaf, err := readRouteRecoveryLeaf(filepath.Join(recovery, entry.Name()))
		if err != nil {
			fmt.Fprintf(&b, "    recovery certificate: %v\n", err)
			continue
		}
		fmt.Fprintf(&b, "    aliases: %s\n    created: %s\n", strings.Join(leaf.DNSNames, ", "), leaf.NotBefore.Local().Format(time.DateOnly))
	}
	if b.Len() == 0 {
		return nil
	}
	_, err = fmt.Fprintf(out, "Installations recorded in %s:\n%sRestore one with tfsaz route restore --installation ID.\n", recovery, b.String())
	return err
}

func routeStateExists(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}

// A concurrent restore may remove the empty recovery directory between the
// MkdirAll and the lock, so recreate it once.
func acquireRouteRecoveryLock(recovery, path string) (func() error, error) {
	for attempt := 0; ; attempt++ {
		if err := os.MkdirAll(recovery, 0700); err != nil {
			return nil, err
		}
		unlock, err := acquireSetupLock(path)
		if err == nil || attempt > 0 || !errors.Is(err, os.ErrNotExist) {
			return unlock, err
		}
	}
}

// removeEmptyRouteRecovery removes the recovery directory and ~/.tfsaz once no
// installation keeps a file there. A directory that still has entries stays,
// so the removal errors are expected and carry no information.
func removeEmptyRouteRecovery(recovery string) {
	if os.Remove(recovery) == nil {
		_ = os.Remove(filepath.Dir(recovery))
	}
}

func retireRouteIdentity(dir string) (err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case routeIdentityName, routeKeyName, routeCertName, routeSystemStateName, "redirect.log", "route-task.xml":
		default:
			return fmt.Errorf("system routing removed; move the extra file %s and retry restore", filepath.Join(dir, entry.Name()))
		}
	}
	temp, err := os.MkdirTemp(filepath.Dir(dir), ".tfsaz-restored-")
	if err != nil {
		return err
	}
	defer removeStaged(temp, &err)
	retired := filepath.Join(temp, "identity")
	if err := os.Rename(dir, retired); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(retired, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(retired)
}
