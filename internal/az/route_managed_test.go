package az

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestManagedRouteRetryAndOfflineRestore(t *testing.T) {
	file, registry, alias, _ := redirectTestSetup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TFSAZ_CONFIG", file)
	var fingerprint string
	installCalls, restoreCalls := 0, 0
	system := func(ctx context.Context, install bool, dir, configFile, cert, key, sha string, aliases []string) error {
		if configFile != file || !slices.Equal(aliases, []string{alias}) || len(sha) != 64 {
			t.Fatal("wrong configuration, alias or certificate")
		}
		if fingerprint != "" && fingerprint != sha {
			t.Fatal("retry replaced the identity")
		}
		fingerprint = sha
		if install {
			installCalls++
			managerTestWrite(t, filepath.Join(dir, routeSystemStateName), "{}")
			managerTestWrite(t, filepath.Join(dir, "redirect.log"), "started")

			if _, _, err := loadRedirectCertificate(cert, key, map[string]bool{alias: true}, time.Now()); err != nil {
				t.Fatal(err)
			}
			if installCalls == 1 {
				return errors.New("OS step interrupted")
			}
		} else {
			restoreCalls++
			if restoreCalls == 1 {
				return errors.New("trust store unavailable")
			}
		}
		return nil
	}
	run := func(action string) error { return runManagedRoute(t.Context(), []string{action}, io.Discard, system) }
	if err := run("setup"); err == nil || !strings.Contains(err.Error(), "route restore") {
		t.Fatalf("interrupted setup: %v", err)
	}
	if err := run("setup"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, registry, filepath.Join(file+".browser-route", routeKeyName)} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := run("restore"); err == nil {
		t.Fatal("ignored OS cleanup failure")
	}
	if err := run("restore"); err != nil {
		t.Fatal(err)
	}
	if err := run("restore"); err != nil || installCalls != 2 || restoreCalls != 2 {
		t.Fatalf("repeat restore: %v, OS calls %d/%d", err, installCalls, restoreCalls)
	}
	if _, err := os.Stat(file + ".browser-route"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory after restore: %v", err)
	}
	// The repeat restore found nothing installed and must not recreate the
	// recovery directory or its lock.
	if _, err := os.Lstat(filepath.Join(home, ".tfsaz")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery directory after the last restore: %v", err)
	}
}

func TestManagedRouteRestoreAcceptsEquivalentConfigPath(t *testing.T) {
	file, _, _, _ := redirectTestSetup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sep := string(filepath.Separator)
	t.Setenv("TFSAZ_CONFIG", filepath.Dir(file)+sep+"."+sep+filepath.Base(file))
	var installed, restored string
	system := func(_ context.Context, install bool, _, _, _, _, sha string, _ []string) error {
		if install {
			installed = sha
		} else {
			restored = sha
		}
		return nil
	}
	if err := runManagedRoute(t.Context(), []string{"setup"}, io.Discard, system); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TFSAZ_CONFIG", file)
	if err := runManagedRoute(t.Context(), []string{"restore"}, io.Discard, system); err != nil || restored == "" || restored != installed {
		t.Fatalf("restore under another spelling of the same path: %v", err)
	}
}

func TestManagedRouteUnsupportedSystemWritesNothing(t *testing.T) {
	file, _, _, _ := redirectTestSetup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TFSAZ_CONFIG", file)
	saved := routeSystemPlatform
	t.Cleanup(func() { routeSystemPlatform = saved })
	routeSystemPlatform = func() error { return errRouteSystemUnsupported }
	system := func(context.Context, bool, string, string, string, string, string, []string) error {
		return errRouteSystemUnsupported
	}
	for _, action := range []string{"setup", "restore"} {
		if err := runManagedRoute(t.Context(), []string{action}, io.Discard, system); !errors.Is(err, errRouteSystemUnsupported) {
			t.Fatalf("route %s on an unsupported system: %v", action, err)
		}
	}
	for _, path := range []string{file + ".browser-route", filepath.Join(home, ".tfsaz")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported setup wrote %s: %v", path, err)
		}
	}
}

func TestManagedRouteRefusesDriftAndForeignFiles(t *testing.T) {
	file, _, _, _ := redirectTestSetup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TFSAZ_CONFIG", file)
	calls := 0
	system := func(context.Context, bool, string, string, string, string, string, []string) error {
		calls++
		return nil
	}
	run := func(action string) error { return runManagedRoute(t.Context(), []string{action}, io.Discard, system) }
	if err := run("setup"); err != nil {
		t.Fatal(err)
	}
	dir := file + ".browser-route"
	foreign := filepath.Join(dir, "my-file")
	managerTestWrite(t, foreign, "untouched")
	if err := run("restore"); err == nil {
		t.Fatal("expected extra-file diagnostic")
	}
	if b, err := os.ReadFile(foreign); err != nil || string(b) != "untouched" {
		t.Fatal("foreign file modified")
	}
	managerTestWrite(t, filepath.Join(dir, routeCertName), "unrelated")
	if err := run("restore"); err == nil || calls != 2 {
		t.Fatalf("certificate drift reached OS integration: err %v, calls %d", err, calls)
	}
}

func TestManagedRouteUsageBeforeConfig(t *testing.T) {
	t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	for _, args := range [][]string{{"setup", "--unknown"}, {"restore", "extra"}, {"verify", "--bad"}, {"verify", "a", "b"}} {
		if err := runManagedRoute(t.Context(), args, io.Discard, nil); ExitCode(err) != 2 {
			t.Errorf("%v: got %v", args, err)
		}
	}
}

func TestManagedRouteMissingInstallAndChangedAliasSet(t *testing.T) {
	file, registry, _, _ := redirectTestSetup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TFSAZ_CONFIG", file)
	run := func(action string) error {
		return runManagedRoute(t.Context(), []string{action}, io.Discard,
			func(context.Context, bool, string, string, string, string, string, []string) error { return nil })
	}
	if err := run("verify"); err == nil {
		t.Fatal("absent route verified")
	}
	if _, err := os.Stat(file + ".browser-route.lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("verify wrote to the configuration directory")
	}
	if err := run("setup"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfigAt(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Profiles[0].Aliases = append(cfg.Profiles[0].Aliases, "https://"+routeCheckProfileAlias)
	updated, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, file, string(updated))
	if err := run("setup"); err == nil || !strings.Contains(err.Error(), "aliases have changed") {
		t.Fatalf("changed alias set silently accepted: %v", err)
	}
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}
	if err := run("restore"); err != nil {
		t.Fatal(err)
	}
	if err := run("setup"); err != nil {
		t.Fatal(err)
	}
	id, err := readRouteIdentity(file+".browser-route", file)
	if err != nil || !slices.Equal(id.Aliases, []string{routeCheckProfileAlias}) {
		t.Fatalf("new identity aliases %v, err %v", id.Aliases, err)
	}
}

func TestRestoreAfterDeletingConfigDirectory(t *testing.T) {
	file, _, alias, _ := redirectTestSetup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TFSAZ_CONFIG", file)
	var fingerprint string
	restored := false
	system := func(_ context.Context, install bool, _, _, _, _, sha string, aliases []string) error {
		if install {
			fingerprint = sha
		} else {
			restored = sha == fingerprint && slices.Equal(aliases, []string{alias})
		}
		return nil
	}
	if err := runManagedRoute(t.Context(), []string{"setup"}, io.Discard, system); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(file)); err != nil {
		t.Fatal(err)
	}
	if err := runManagedRoute(t.Context(), []string{"restore"}, io.Discard, system); err != nil || !restored {
		t.Fatalf("missing-config recovery: restored=%v err=%v", restored, err)
	}
}
