package az

import (
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
	"slices"
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

func stageRouteIdentity(dir, configFile string, aliases []string) (routeIdentity, error) {
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
	defer os.RemoveAll(staged)
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
	if json.Unmarshal(data, &identity) != nil || identity.ConfigFile != configFile || len(identity.Aliases) == 0 {
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

type routeSystemOperation func(context.Context, bool, string, string, string, string, string, []string) error

func runManagedRoute(ctx context.Context, args []string, out io.Writer, system routeSystemOperation) (err error) {
	action := args[0]
	var rawURL string
	if action == "verify" && len(args) == 2 {
		rawURL = args[1]
		if rawURL == "" || rawURL[0] == '-' {
			return usageError{errors.New("expected route verify [HTTPS_PAGE_URL]")}
		}
	} else if len(args) != 1 {
		return usageError{fmt.Errorf("unexpected arguments for route %s", action)}
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
	if err := os.MkdirAll(recovery, 0700); err != nil {
		return err
	}
	receipt := filepath.Join(recovery, routeSystemID(file)+".pem")
	unlock, err := acquireSetupLock(receipt + ".lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	identity, readErr := readRouteIdentity(dir, file)
	missing := errors.Is(readErr, os.ErrNotExist)
	if readErr != nil && !missing {
		return readErr
	}
	certFile, keyFile := filepath.Join(dir, routeCertName), filepath.Join(dir, routeKeyName)
	if action == "restore" {
		if missing {
			der, err := readRouteSystemLeaf(receipt)
			if errors.Is(err, os.ErrNotExist) {
				_, err = fmt.Fprintln(out, "No installation recorded for this configuration. If you manually deleted both the route directory and the recovery certificate, inspect OS routing and trust before using the alias.")
				return err
			}
			if err != nil {
				return err
			}
			leaf, err := x509.ParseCertificate(der)
			if err != nil || !leaf.BasicConstraintsValid || leaf.IsCA || len(leaf.DNSNames) == 0 {
				return errors.New("invalid browser route recovery certificate")
			}
			names := make(map[string]bool, len(leaf.DNSNames))
			for _, alias := range leaf.DNSNames {
				names[alias] = generatedAliasHost.MatchString(alias)
			}
			// Expired installations must still be removable.
			if _, err := validateRedirectLeaf(leaf, names, leaf.NotBefore); err != nil {
				return err
			}
			if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
				return errors.New("the browser route recovery certificate must be self-signed")
			}
			identity.SHA256, identity.Aliases = fingerprintHex(der), leaf.DNSNames
		}
		if err := system(ctx, false, dir, file, certFile, keyFile, identity.SHA256, identity.Aliases); err != nil {
			return fmt.Errorf("restore browser routing: %w; ownership information retained for retry", err)
		}
		// Atomically retire the identity before deleting its files. Never recurse
		// into the installation directory, which may contain user-created files.
		if !missing {
			if err := retireRouteIdentity(dir); err != nil {
				return err
			}
		}
		if err := os.Remove(receipt); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err = fmt.Fprintln(out, "Restored browser routing.")
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
	if err := system(ctx, true, dir, file, certFile, keyFile, identity.SHA256, identity.Aliases); err != nil {
		return fmt.Errorf("configure browser routing: %w; rerun tfsaz route setup or tfsaz route restore", err)
	}
	_, err = fmt.Fprintln(out, "Browser routing configured. Run tfsaz route verify with a PR URL and open it in your intended browser.")
	return err
}

func retireRouteIdentity(dir string) error {
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
	defer os.Remove(temp)
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
