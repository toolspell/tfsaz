//go:build linux

package az

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Linux: a systemd user service runs the redirect service on a high loopback
// port. A root oneshot unit that runs only nft loads a private nftables table
// at boot that sends loopback port 443 there. Chrome, Chromium and Electron
// trust the leaf through each existing per-user NSS database ("P,," marks a
// trusted peer, not a CA). Prerequisites: a systemd user session, sudo,
// nftables (nft) and, for trust, certutil from libnss3-tools.

func linuxUserUnit(id string) string { return "tfsaz-route-redirect-" + id + ".service" }
func linuxNATUnit(id string) string  { return "tfsaz-route-redirect-" + id + "-nat.service" }
func linuxNATTable(id string) string { return "tfsaz_route_" + id }
func linuxNATFile(id string) string  { return "/etc/tfsaz-route-" + id + ".nft" }
func linuxNSSNick(id string) string  { return "tfsaz-route-redirect-" + id }
func linuxNATUnitPath(id string) string {
	return "/etc/systemd/system/" + linuxNATUnit(id)
}

func linuxUserUnitPath(id string) (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", linuxUserUnit(id)), nil
}

func routeSystemLogHint(p routeSystemPlan) string {
	return "see journalctl --user -u " + linuxUserUnit(p.id)
}

func routeSystemInstallOS(ctx context.Context, p routeSystemPlan) error {
	if _, err := runRouteSystemCommand(ctx, nil, "systemctl", "--user", "show-environment"); err != nil {
		return fmt.Errorf("browser routing needs a systemd user session: %w", err)
	}
	nft, err := routeSystemRootTool("/usr/sbin/nft", "/sbin/nft")
	if err != nil {
		return fmt.Errorf("browser routing needs nftables (install the nftables package): %w", err)
	}
	dbs := linuxNSSDBs(false)
	if len(dbs) == 0 {
		return errors.New("no Chrome or Chromium certificate database (cert9.db) was found; start the browser once, then retry")
	}
	certutil, err := exec.LookPath("certutil")
	if err != nil {
		return errors.New("browser routing needs certutil to trust the leaf; install libnss3-tools")
	}
	hosts, err := readRouteHosts()
	if err != nil {
		return err
	}
	if err := routeHostsConflict(hosts, p.id, p.aliases); err != nil {
		return err
	}
	if err := installLinuxUserUnit(ctx, p); err != nil {
		return err
	}
	if err := waitRouteSystemService(ctx, p, 15*time.Second); err != nil {
		return err
	}
	// Checked after the restart so this setup's own earlier leaf is not mistaken
	// for another program after a certificate renewal.
	if err := checkRouteSystem443(ctx, p); err != nil {
		return err
	}
	if _, err := linuxRouteDBReceipt(p.id, dbs); err != nil {
		return err
	}
	for _, db := range dbs {
		if err := installLinuxNSS(ctx, certutil, db, p); err != nil {
			return err
		}
	}
	return runRouteSystemPrivileged(ctx, linuxPrivilegedInstall(p, nft))
}

func routeSystemRestoreOS(ctx context.Context, p routeSystemPlan) error {
	var errs []error
	if err := removeLinuxUserUnit(ctx, p.id); err != nil {
		errs = append(errs, err)
	}
	hosts, err := readRouteHosts()
	if err != nil {
		errs = append(errs, err)
	}
	if linuxPrivilegedTraces(p.id, hosts) {
		if err := runRouteSystemPrivileged(ctx, linuxPrivilegedRestore(p.id)); err != nil {
			errs = append(errs, err)
		}
	}
	if err := removeLinuxNSSAll(ctx, p); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(home, ".tfsaz", "route-recovery", p.id+".nss.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// removeLinuxNSSAll checks the databases in the receipt and the ones the
// browsers use now, so a lost receipt cannot strand a trust entry.
func removeLinuxNSSAll(ctx context.Context, p routeSystemPlan) error {
	recorded, err := linuxRouteDBReceipt(p.id, nil)
	if err != nil {
		return err
	}
	dbs := append(slices.Clone(recorded), linuxNSSDBs(true)...)
	slices.Sort(dbs)
	dbs = slices.Compact(dbs)
	if len(dbs) == 0 {
		return nil
	}
	certutil, err := exec.LookPath("certutil")
	if err != nil {
		// Setup needs certutil before it records a database, so only a
		// recorded one can hold an entry that is now out of reach.
		if len(recorded) > 0 {
			return fmt.Errorf("cannot check %s for the routing certificate without certutil (libnss3-tools)", recorded[0])
		}
		return nil
	}
	var errs []error
	for _, db := range dbs {
		if err := removeLinuxNSS(ctx, certutil, db, p); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Record the actual database directories before modifying trust. XDG_DATA_HOME
// or the current snap revision may differ when restore runs.
func linuxRouteDBReceipt(id string, add []string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".tfsaz", "route-recovery", id+".nss.json")
	var dbs []string
	data, err := readSetupFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &dbs); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(add) > 0 {
		dbs = append(dbs, add...)
		slices.Sort(dbs)
		dbs = slices.Compact(dbs)
		data, err = json.Marshal(dbs)
		if err != nil {
			return nil, err
		}
		if err := publishSetupFile(path, data, true); err != nil {
			return nil, err
		}
	}
	return dbs, nil
}

func linuxPrivilegedTraces(id, hosts string) bool {
	for _, path := range []string{linuxNATUnitPath(id), linuxNATFile(id)} {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return ownsRoutePort(id) || hosts == "" || routeHostsHasMarker(hosts, id)
}

// systemdQuote quotes one ExecStart or Environment word. Unit files expand %
// specifiers everywhere and $ variables in ExecStart.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$")
	return `"` + r.Replace(s) + `"`
}

func linuxUserUnitContent(p routeSystemPlan) (string, error) {
	for _, s := range []string{p.exe, p.configFile, p.certFile, p.keyFile} {
		if strings.ContainsAny(s, "\n\r") {
			return "", errors.New("routing paths must not contain line breaks")
		}
	}
	env := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace("TFSAZ_CONFIG=" + p.configFile)
	return fmt.Sprintf(`[Unit]
Description=TFSaz browser routing redirect (%s)

[Service]
Environment="%s"
ExecStart=%s route redirect --cert %s --key %s --port %d
Restart=always
RestartSec=5
NoNewPrivileges=yes

[Install]
WantedBy=default.target
`, p.id, env, systemdQuote(p.exe), systemdQuote(p.certFile), systemdQuote(p.keyFile), p.port), nil
}

func installLinuxUserUnit(ctx context.Context, p routeSystemPlan) error {
	path, err := linuxUserUnitPath(p.id)
	if err != nil {
		return err
	}
	content, err := linuxUserUnitContent(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write the systemd user unit: %w", err)
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", linuxUserUnit(p.id)},
		{"--user", "restart", linuxUserUnit(p.id)},
	} {
		if _, err := runRouteSystemCommand(ctx, nil, "systemctl", args...); err != nil {
			return err
		}
	}
	return nil
}

func removeLinuxUserUnit(ctx context.Context, id string) error {
	path, err := linuxUserUnitPath(id)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := runRouteSystemCommand(ctx, nil, "systemctl", "--user", "disable", "--now", linuxUserUnit(id)); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the systemd user unit: %w", err)
	}
	_, err = runRouteSystemCommand(ctx, nil, "systemctl", "--user", "daemon-reload")
	return err
}

func linuxNATRules(id string, port int) string {
	t := linuxNATTable(id)
	p := strconv.Itoa(port)
	uid := strconv.Itoa(os.Getuid())
	// Declaring the table first makes the delete succeed on a fresh boot.
	return "table inet " + t + "\ndelete table inet " + t + "\ntable inet " + t + ` {
	chain output {
		type nat hook output priority -100; policy accept;
		oifname "lo" ip daddr 127.0.0.1 meta skuid ` + uid + ` tcp dport 443 redirect to :` + p + `
		oifname "lo" ip6 daddr ::1 meta skuid ` + uid + ` tcp dport 443 redirect to :` + p + `
	}
}
`
}

func linuxPrivilegedInstall(p routeSystemPlan, nft string) string {
	unit := `[Unit]
Description=TFSaz browser routing loopback port 443 redirect (` + p.id + `)
After=nftables.service firewalld.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + nft + ` -f ` + linuxNATFile(p.id) + `
ExecStop=` + nft + ` delete table inet ` + linuxNATTable(p.id) + `

[Install]
WantedBy=multi-user.target
`
	return reserveRoutePort(p.id) + writeRootFile(linuxNATFile(p.id), linuxNATRules(p.id, p.port), "root") +
		shellQuote(nft) + " -c -f " + linuxNATFile(p.id) + "\n" +
		writeRootFile(linuxNATUnitPath(p.id), unit, "root") +
		"systemctl daemon-reload\n" +
		"systemctl enable " + linuxNATUnit(p.id) + "\n" +
		"systemctl restart " + linuxNATUnit(p.id) + "\n" +
		routeHostsShell(routeHostsPath(), p.id, p.aliases, true)
}

func linuxPrivilegedRestore(id string) string {
	return "if [ -f " + linuxNATUnitPath(id) + " ]; then systemctl disable --now " + linuxNATUnit(id) + "; fi\n" +
		"rm -f " + linuxNATUnitPath(id) + "\n" +
		"systemctl daemon-reload\n" +
		"tables=$(nft list tables)\nif printf '%s\\n' \"$tables\" | grep -Fxq " + shellQuote("table inet "+linuxNATTable(id)) + "; then nft delete table inet " + linuxNATTable(id) + "; fi\n" +
		"rm -f " + linuxNATFile(id) + "\n" +
		routeHostsShell(routeHostsPath(), id, nil, false) + releaseRoutePort(id)
}

// linuxNSSDBs lists existing per-user NSS databases that Chromium-based
// browsers read, including snap Chromium's private one. Like NSS, it uses an
// absolute XDG_DATA_HOME instead of ~/.local/share. Restore also checks the
// default location so that a changed XDG_DATA_HOME cannot strand an entry.
func linuxNSSDBs(includeDefault bool) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	defaultData := filepath.Join(home, ".local", "share")
	data := defaultData
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		data = xdg
	}
	candidates := []string{filepath.Join(home, ".pki", "nssdb"), filepath.Join(data, "pki", "nssdb"),
		filepath.Join(home, "snap", "chromium", "current", ".local", "share", "pki", "nssdb"),
		filepath.Join(home, "snap", "chromium", "current", ".pki", "nssdb")}
	if includeDefault && data != defaultData {
		candidates = append(candidates, filepath.Join(defaultData, "pki", "nssdb"))
	}
	var dbs []string
	seen := make(map[string]bool)
	for _, dir := range candidates {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || seen[real] {
			continue
		}
		if _, err := os.Stat(filepath.Join(real, "cert9.db")); err == nil {
			seen[real] = true
			dbs = append(dbs, real)
		}
	}
	return dbs
}

// linuxNSSNicknames lists the nicknames in certutil -L output. The trust
// attributes are the last word; nicknames may contain spaces.
func linuxNSSNicknames(out []byte) []string {
	var nicks []string
	for line := range strings.Lines(string(out)) {
		line = strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "Certificate Nickname") || strings.HasPrefix(trimmed, "SSL,S/MIME") {
			continue
		}
		i := strings.LastIndexAny(trimmed, " \t")
		if i < 0 {
			continue
		}
		if nick := strings.TrimSpace(trimmed[:i]); nick != "" {
			nicks = append(nicks, nick)
		}
	}
	return nicks
}

// linuxNSSForeign returns another nickname that already holds the leaf. NSS
// keeps one object per certificate, so adding or deleting under this setup's
// nickname would change or remove someone else's trust entry.
func linuxNSSForeign(ctx context.Context, certutil, db, nick, leafSHA256 string) (string, error) {
	out, err := runRouteSystemCommand(ctx, nil, certutil, "-d", "sql:"+db, "-L")
	if err != nil {
		return "", fmt.Errorf("list the certificates in %s: %w", db, err)
	}
	for _, other := range linuxNSSNicknames(out) {
		if other != nick && slices.Contains(linuxNSSCopies(ctx, certutil, db, other), leafSHA256) {
			return other, nil
		}
	}
	return "", nil
}

// linuxNSSCopies returns fingerprints stored under this setup's nickname.
func linuxNSSCopies(ctx context.Context, certutil, db, nick string) []string {
	out, err := runRouteSystemCommand(ctx, nil, certutil, "-d", "sql:"+db, "-L", "-n", nick, "-a")
	if err != nil {
		return nil
	}
	var fps []string
	for _, der := range pemCertificates(out) {
		fps = append(fps, fingerprintHex(der))
	}
	return fps
}

func installLinuxNSS(ctx context.Context, certutil, db string, p routeSystemPlan) error {
	nick := linuxNSSNick(p.id)
	copies := linuxNSSCopies(ctx, certutil, db, nick)
	if len(copies) == 1 && copies[0] == p.leafSHA256 {
		return nil
	}
	if other, err := linuxNSSForeign(ctx, certutil, db, nick, p.leafSHA256); err != nil {
		return err
	} else if other != "" {
		return fmt.Errorf("%s already holds the routing certificate as %q; remove that entry first", db, other)
	}
	if len(copies) > 0 {
		return fmt.Errorf("certificate nickname %s in %s belongs to another leaf", nick, db)
	}
	if _, err := runRouteSystemCommand(ctx, nil, certutil, "-d", "sql:"+db, "-A", "-t", "P,,", "-n", nick, "-i", p.certFile); err != nil {
		return fmt.Errorf("trust the routing certificate in %s: %w", db, err)
	}
	if copies := linuxNSSCopies(ctx, certutil, db, nick); len(copies) != 1 || copies[0] != p.leafSHA256 {
		return fmt.Errorf("the routing certificate is not listed in %s after adding it", db)
	}
	return nil
}

func removeLinuxNSS(ctx context.Context, certutil, db string, p routeSystemPlan) error {
	if _, err := os.Stat(filepath.Join(db, "cert9.db")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	nick := linuxNSSNick(p.id)
	list, err := runRouteSystemCommand(ctx, nil, certutil, "-d", "sql:"+db, "-L")
	if err != nil {
		return err
	}
	if !slices.Contains(linuxNSSNicknames(list), nick) {
		return nil
	}
	copies := linuxNSSCopies(ctx, certutil, db, nick)
	if len(copies) != 1 || copies[0] != p.leafSHA256 {
		return fmt.Errorf("certificate nickname %s in %s differs from the managed fingerprint; preserving the entry", nick, db)
	}
	_, err = runRouteSystemCommand(ctx, nil, certutil, "-d", "sql:"+db, "-D", "-n", nick)
	return err
}

// writeRootFile is a shell fragment that replaces path with root-owned content
// through a temporary file, so an interruption never leaves a partial file.
func writeRootFile(path, content, group string) string {
	if strings.Contains(content, "\nTFSAZ_EOF\n") {
		panic("heredoc terminator in content")
	}
	q := shellQuote(path)
	return fmt.Sprintf(`f=%s
tmp=$(mktemp "$f.tfsaz.XXXXXX")
trap 'rm -f "$tmp"' EXIT
cat > "$tmp" <<'TFSAZ_EOF'
%sTFSAZ_EOF
chown root:%s "$tmp"
chmod 644 "$tmp"
mv -f "$tmp" "$f"
trap - EXIT
`, q, content, group)
}

const routePortOwner = "/etc/tfsaz-browser-port-443.owner"

// Port forwarding is shared by all user sessions, including while the user's
// redirect process is stopped. Reserve it independently of listener readiness.
func reserveRoutePort(id string) string {
	return fmt.Sprintf(`f=%s
if [ ! -e "$f" ]; then
  tmp=$(mktemp "$f.XXXXXX")
  trap 'rm -f "$tmp"' EXIT
  printf '%%s\n' %s > "$tmp"
  chmod 644 "$tmp"
  ln "$tmp" "$f" 2>/dev/null || true
  rm -f "$tmp"; trap - EXIT
fi
[ ! -L "$f" ] && [ "$(cat "$f")" = %s ] || { echo "loopback port 443 is reserved by another managed installation" >&2; exit 1; }
`, routePortOwner, shellQuote(id), shellQuote(id))
}

func ownsRoutePort(id string) bool {
	data, err := os.ReadFile(routePortOwner)
	return err == nil && strings.TrimSpace(string(data)) == id
}

func releaseRoutePort(id string) string {
	return fmt.Sprintf("if [ -f %s ] && [ \"$(cat %s)\" = %s ]; then rm %s; fi\n", routePortOwner, routePortOwner, shellQuote(id), routePortOwner)
}
