//go:build linux

package az

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestLinuxUserUnitQuoting(t *testing.T) {
	p := routeSystemPlan{id: routeTestID, exe: "/opt/t fs/tfsaz", configFile: `/home/a b/100%/$x".json`,
		certFile: "/home/u/r/cert.pem", keyFile: "/home/u/r/key.pem", port: 40123}
	unit, err := linuxUserUnitContent(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Environment="TFSAZ_CONFIG=/home/a b/100%%/$x\".json"`,
		`ExecStart="/opt/t fs/tfsaz" route redirect --cert "/home/u/r/cert.pem" --key "/home/u/r/key.pem" --port 40123`,
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %s:\n%s", want, unit)
		}
	}
	p.keyFile = "/x\n[Service]"
	if _, err := linuxUserUnitContent(p); err == nil {
		t.Fatal("accepted a line break")
	}
}

func TestLinuxPrivilegedScripts(t *testing.T) {
	p := routeSystemPlan{id: routeTestID, aliases: []string{routeTestAlias}, port: 40123, exe: "/home/u/bin/tfsaz", dir: "/home/u/r"}
	s := linuxPrivilegedInstall(p, "/usr/sbin/nft")
	for _, want := range []string{"meta skuid " + strconv.Itoa(os.Getuid()) + " tcp dport 443", "redirect to :40123", `oifname "lo" ip6 daddr ::1`, "ExecStart=/usr/sbin/nft -f " + linuxNATFile(p.id),
		"systemctl enable " + linuxNATUnit(p.id), "::1 " + routeTestAlias + " # tfsaz-route " + p.id} {
		if !strings.Contains(s, want) {
			t.Errorf("install script lacks %q", want)
		}
	}
	if strings.Contains(s, "/home/u") {
		t.Error("install script references the user's files")
	}
	r := linuxPrivilegedRestore(p.id)
	if !strings.Contains(r, "delete table inet "+linuxNATTable(p.id)) || strings.Contains(r, "flush ruleset") {
		t.Errorf("restore script:\n%s", r)
	}
}

func TestLinuxNSSDBsFollowXDGDataHome(t *testing.T) {
	home, xdg := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdg)
	def, own := filepath.Join(home, ".local", "share", "pki", "nssdb"), filepath.Join(xdg, "pki", "nssdb")
	for _, dir := range []string{def, own} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cert9.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	real := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	if got := linuxNSSDBs(false); !slices.Equal(got, []string{real(own)}) {
		t.Errorf("install databases %v; want only %s", got, own)
	}
	if got := linuxNSSDBs(true); !slices.Equal(got, []string{real(own), real(def)}) {
		t.Errorf("restore databases %v; want %s and %s", got, own, def)
	}
}

func TestLinuxNSSNicknames(t *testing.T) {
	out := "\nCertificate Nickname                                         Trust Attributes\n" +
		"                                                             SSL,S/MIME,JAR/XPI\n\n" +
		"My Server Leaf                                               P,,  \n" +
		"tfsaz-route-redirect-0123456789abcdef                        P,,\n"
	if got := linuxNSSNicknames([]byte(out)); !slices.Equal(got, []string{"My Server Leaf", "tfsaz-route-redirect-0123456789abcdef"}) {
		t.Fatalf("nicknames %q", got)
	}
}

func TestLinuxNSSRefusesForeignCopyOfLeaf(t *testing.T) {
	der := []byte("fixture leaf bytes")
	pemLeaf := string(routeSystemPEM(der))
	p := routeSystemPlan{id: routeTestID, leafSHA256: fingerprintHex(der), certFile: "/r/alias.pem"}
	calls := fakeRouteSystemCommands(t, func(c routeSystemCall) ([]byte, error) {
		switch {
		case slices.Equal(c.args, []string{"-d", "sql:/db", "-L"}):
			return []byte("Certificate Nickname  Trust Attributes\nmanual alias  P,,\n"), nil
		case slices.Contains(c.args, "manual alias"):
			return []byte(pemLeaf), nil
		case slices.Contains(c.args, "-A"), slices.Contains(c.args, "-D"):
			t.Fatalf("changed the database: %v", c.args)
		}
		return nil, errors.New("not found")
	})
	err := installLinuxNSS(t.Context(), "certutil", "/db", p)
	if err == nil || !strings.Contains(err.Error(), `"manual alias"`) {
		t.Fatalf("install over a foreign entry: %v (calls %v)", err, *calls)
	}
}

func TestLinuxRestoreChecksCurrentNSSDatabases(t *testing.T) {
	home, xdg, bin := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "certutil"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(xdg, "pki", "nssdb")
	if err := os.MkdirAll(db, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db, "cert9.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := filepath.EvalSymlinks(db)
	if err != nil {
		t.Fatal(err)
	}
	der := []byte("fixture leaf bytes")
	p := routeSystemPlan{id: routeTestID, leafSHA256: fingerprintHex(der)}
	nick := linuxNSSNick(p.id)
	deleted := false
	fakeRouteSystemCommands(t, func(c routeSystemCall) ([]byte, error) {
		switch {
		case slices.Equal(c.args, []string{"-d", "sql:" + db, "-L"}):
			return []byte("Certificate Nickname  Trust Attributes\n" + nick + "  P,,\n"), nil
		case slices.Equal(c.args, []string{"-d", "sql:" + db, "-L", "-n", nick, "-a"}):
			return routeSystemPEM(der), nil
		case slices.Equal(c.args, []string{"-d", "sql:" + db, "-D", "-n", nick}):
			deleted = true
			return nil, nil
		}
		t.Fatalf("unexpected command %s %v", c.name, c.args)
		return nil, nil
	})
	// No receipt: the recovery directory was deleted while routing was installed.
	if err := routeSystemRestoreOS(t.Context(), p); err != nil || !deleted {
		t.Fatalf("restore without a receipt: err %v, entry removed %v", err, deleted)
	}
}
