//go:build darwin

package az

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const macSecurity = "/usr/bin/security"

// sslServer policy OID 1.2.840.113635.100.1.3 as stored in trust settings.
var macSSLPolicyOID = []byte{0x2a, 0x86, 0x48, 0x86, 0xf7, 0x63, 0x64, 0x01, 0x03}

func macLoginKeychain() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db"), nil
}

// macTrustArgs gives one "-p ssl -s ALIAS" pair per alias. Each pair becomes an
// sslServer rule that applies only when the TLS hostname equals ALIAS.
func macTrustArgs(aliases []string) []string {
	args := []string{"add-trusted-cert", "-r", "trustRoot"}
	for _, a := range aliases {
		args = append(args, "-p", "ssl", "-s", a)
	}
	return args
}

const macEmptyTrustSettings = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>trustList</key><dict/><key>trustVersion</key><integer>1</integer></dict></plist>
`

// checkMacTrustExpression has the system tool render the exact trust settings
// into a temporary file, without touching any trust domain or keychain, and
// refuses to continue unless every rule is SSL-only and names one alias.
func checkMacTrustExpression(ctx context.Context, certFile string, aliases []string) error {
	tmp, err := os.MkdirTemp("", "tfsaz-trust-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	in, out := filepath.Join(tmp, "in.plist"), filepath.Join(tmp, "out.plist")
	if err := os.WriteFile(in, []byte(macEmptyTrustSettings), 0o600); err != nil {
		return err
	}
	args := append(macTrustArgs(aliases), "-i", in, "-o", out, certFile)
	if _, err := runRouteSystemCommand(ctx, nil, macSecurity, args...); err != nil {
		return fmt.Errorf("render the routing trust settings: %w", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return fmt.Errorf("render the routing trust settings: %w", err)
	}
	return validateMacTrustSettings(data, aliases)
}

func validateMacTrustSettings(data []byte, aliases []string) error {
	root, err := decodePlist(data)
	if err != nil {
		return fmt.Errorf("parse rendered trust settings: %w", err)
	}
	refuse := errors.New("macOS would not scope trust to the exact aliases; refusing to add broader trust")
	top, _ := root.(map[string]any)
	list, _ := top["trustList"].(map[string]any)
	if len(list) != 1 {
		return refuse
	}
	var settings []any
	for _, entry := range list {
		e, _ := entry.(map[string]any)
		settings, _ = e["trustSettings"].([]any)
	}
	if len(settings) == 0 {
		return refuse
	}
	covered := make(map[string]bool)
	for _, s := range settings {
		rule, _ := s.(map[string]any)
		policy, _ := rule["kSecTrustSettingsPolicy"].([]byte)
		host, _ := rule["kSecTrustSettingsPolicyString"].(string)
		if !bytes.Equal(policy, macSSLPolicyOID) || !slices.Contains(aliases, host) {
			return refuse
		}
		for key, v := range rule {
			switch key {
			case "kSecTrustSettingsPolicy", "kSecTrustSettingsPolicyString", "kSecTrustSettingsPolicyName":
			case "kSecTrustSettingsResult": // 1 = trustRoot, the default when absent
				if v != int64(1) {
					return refuse
				}
			default: // application, key usage or allowed-error constraints
				return refuse
			}
		}
		covered[host] = true
	}
	if len(covered) != len(aliases) {
		return refuse
	}
	return nil
}

func macTrusted(ctx context.Context, certFile string, aliases []string) bool {
	for _, alias := range aliases {
		if _, err := runRouteSystemCommand(ctx, nil, macSecurity, "verify-cert", "-q", "-L", "-c", certFile, "-p", "ssl", "-s", alias); err != nil {
			return false
		}
	}
	return len(aliases) > 0
}

func installMacTrust(ctx context.Context, p routeSystemPlan) error {
	if macTrusted(ctx, p.certFile, p.aliases) {
		return nil
	}
	if err := checkMacTrustExpression(ctx, p.certFile, p.aliases); err != nil {
		return err
	}
	keychain, err := macLoginKeychain()
	if err != nil {
		return err
	}
	args := append(macTrustArgs(p.aliases), "-k", keychain, p.certFile)
	if _, err := runRouteSystemCommand(ctx, nil, macSecurity, args...); err != nil {
		return fmt.Errorf("trust the routing certificate (approve the macOS prompt in a logged-in desktop session): %w", err)
	}
	if !macTrusted(ctx, p.certFile, p.aliases) {
		return errors.New("macOS does not trust the routing certificate for its aliases after the change; approve the prompt and retry")
	}
	return nil
}

// macKeychainCopies returns the DER of every copy of the leaf in the login keychain.
func macKeychainCopies(ctx context.Context, keychain, sha256Hex string) ([][]byte, error) {
	out, err := runRouteSystemCommand(ctx, nil, macSecurity, "find-certificate", "-a", "-p", keychain)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 { // errSecItemNotFound
			return nil, nil
		}
		return nil, fmt.Errorf("check login keychain for managed certificate: %w", err)
	}
	var found [][]byte
	for _, der := range pemCertificates(out) {
		if fingerprintHex(der) == sha256Hex {
			found = append(found, der)
		}
	}
	return found, nil
}

func removeMacTrust(ctx context.Context, p routeSystemPlan) error {
	keychain, err := macLoginKeychain()
	if err != nil {
		return err
	}
	copies, err := macKeychainCopies(ctx, keychain, p.leafSHA256)
	if err != nil {
		return err
	}
	der := p.certDER
	if len(copies) > 0 {
		der = copies[0]
	}
	if len(der) == 0 || fingerprintHex(der) != p.leafSHA256 {
		return errors.New("missing public certificate for checking user trust settings")
	}
	tmp, err := os.MkdirTemp("", "tfsaz-route-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	certFile := filepath.Join(tmp, "leaf.pem")
	if err := os.WriteFile(certFile, routeSystemPEM(der), 0o600); err != nil {
		return err
	}
	// Trust settings outlive a keychain item, and verification always rejects
	// an expired leaf. Check the actual removal result before deleting anything.
	out, err := runRouteSystemCommand(ctx, nil, macSecurity, "remove-trusted-cert", certFile)
	if err != nil && !macTrustSettingAbsent(err, string(out)) {
		return fmt.Errorf("remove the managed user trust settings and retry restore: %w", err)
	}
	for range copies {
		if _, err := runRouteSystemCommand(ctx, nil, macSecurity, "delete-certificate", "-Z", strings.ToUpper(p.leafSHA256), keychain); err != nil {
			return fmt.Errorf("remove the routing certificate from the login keychain: %w", err)
		}
	}
	return nil
}

func macTrustSettingAbsent(err error, output string) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	switch exit.ExitCode() {
	case 44, 81: // errSecItemNotFound, errSecNoTrustSettings (low byte of OSStatus)
		return true
	}
	// Some security subcommands return EXIT_FAILURE and print the OSStatus.
	for _, line := range strings.Split(output, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), ": No Trust Settings were found.") ||
			strings.HasSuffix(strings.TrimSpace(line), ": The specified item could not be found in the keychain.") {
			return true
		}
	}
	return false
}

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// decodePlist reads an XML property list into maps, slices, strings, int64,
// bool and []byte. Dates stay strings.
func decodePlist(data []byte) (any, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "plist" {
			for {
				tok, err := d.Token()
				if err != nil {
					return nil, err
				}
				if start, ok := tok.(xml.StartElement); ok {
					return decodePlistValue(d, start)
				}
			}
		}
	}
}

func decodePlistValue(d *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		m := make(map[string]any)
		for {
			key, done, err := nextPlistElement(d)
			if err != nil || done {
				return m, err
			}
			if key.Name.Local != "key" {
				return nil, errors.New("plist dict without key")
			}
			var name string
			if err := d.DecodeElement(&name, &key); err != nil {
				return nil, err
			}
			value, done, err := nextPlistElement(d)
			if err != nil || done {
				return nil, errors.New("plist key without value")
			}
			if m[name], err = decodePlistValue(d, value); err != nil {
				return nil, err
			}
		}
	case "array":
		var list []any
		for {
			el, done, err := nextPlistElement(d)
			if err != nil || done {
				return list, err
			}
			v, err := decodePlistValue(d, el)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	case "true", "false":
		return start.Name.Local == "true", d.Skip()
	}
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return nil, err
	}
	switch start.Name.Local {
	case "string", "date":
		return text, nil
	case "integer":
		var n int64
		_, err := fmt.Sscan(text, &n)
		return n, err
	case "data":
		return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text), ""))
	}
	return nil, fmt.Errorf("unsupported plist element %s", start.Name.Local)
}

// nextPlistElement returns the next child element, or done at the parent's end.
func nextPlistElement(d *xml.Decoder) (xml.StartElement, bool, error) {
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return xml.StartElement{}, true, io.ErrUnexpectedEOF
		}
		if err != nil {
			return xml.StartElement{}, true, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return t, false, nil
		case xml.EndElement:
			return xml.StartElement{}, true, nil
		}
	}
}
