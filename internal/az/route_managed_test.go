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
	system := func(ctx context.Context, install bool, _, dir, configFile, cert, key, sha string, aliases []string) error {
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
	system := func(_ context.Context, install bool, _, _, _, _, _, sha string, _ []string) error {
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

// Restore under another configuration path lists the recorded installations
// and changes nothing; --installation restores the chosen one.
func TestManagedRouteRestoreOtherConfigInstallation(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		t.Run(map[bool]string{true: "recorded path", false: "older installation"}[recorded], func(t *testing.T) {
			file, _, alias, _ := redirectTestSetup(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("TFSAZ_CONFIG", file)
			id := routeSystemID(file)
			type call struct {
				install            bool
				id, dir, file, sha string
				aliases            []string
			}
			var calls []call
			system := func(_ context.Context, install bool, id, dir, configFile, _, _, sha string, aliases []string) error {
				calls = append(calls, call{install, id, dir, configFile, sha, aliases})
				return nil
			}
			run := func(args ...string) (string, error) {
				var out strings.Builder
				err := runManagedRoute(t.Context(), args, &out, system)
				return out.String(), err
			}
			if _, err := run("setup"); err != nil {
				t.Fatal(err)
			}
			recovery := filepath.Join(home, ".tfsaz", "route-recovery")
			record := filepath.Join(recovery, id+routeConfigRecordSuffix)
			if got, err := readRouteConfigRecord(recovery, id); err != nil || got != file {
				t.Fatalf("recorded configuration %q, err %v", got, err)
			}
			if !recorded {
				if err := os.Remove(record); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			configLine := "configuration: " + file
			if !recorded {
				configLine = "configuration: not recorded"
			}
			leaf, err := readRouteRecoveryLeaf(filepath.Join(recovery, id+".pem"))
			if err != nil {
				t.Fatal(err)
			}
			out, err := run("restore")
			for _, want := range []string{routeNothingRecorded, id, configLine, "aliases: " + alias, "created: " + leaf.NotBefore.Local().Format(time.DateOnly), "--installation"} {
				if !strings.Contains(out, want) {
					t.Errorf("listing lacks %q:\n%s", want, out)
				}
			}
			if err != nil || len(calls) != 1 {
				t.Fatalf("listing restore: err %v, OS calls %d", err, len(calls))
			}
			for _, args := range [][]string{{"restore", "--installation"}, {"restore", "--installation", "ABC"}, {"restore", "--id", id}} {
				if _, err := run(args...); ExitCode(err) != 2 {
					t.Errorf("%v: got %v", args, err)
				}
			}
			out, err = run("restore", "--installation", id)
			if err != nil || len(calls) != 2 || !strings.Contains(out, "Restored browser routing.") {
				t.Fatalf("restore by ID: err %v, OS calls %d, output %s", err, len(calls), out)
			}
			got, want := calls[1], call{false, id, file + ".browser-route", file, calls[0].sha, []string{alias}}
			if !recorded {
				want.dir, want.file = "", ""
			}
			if got.install != want.install || got.id != want.id || got.dir != want.dir || got.file != want.file || got.sha != want.sha || !slices.Equal(got.aliases, want.aliases) {
				t.Fatalf("restore by ID passed %+v, want %+v", got, want)
			}
			if !recorded {
				// The route directory beside the unrecorded path stays until
				// restore runs with that configuration.
				if !strings.Contains(out, ".browser-route") {
					t.Errorf("restore without a recorded path does not mention the route directory:\n%s", out)
				}
				t.Setenv("TFSAZ_CONFIG", file)
				if _, err := run("restore"); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{file + ".browser-route", filepath.Join(home, ".tfsaz")} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s after restore: %v", path, err)
				}
			}
		})
	}
}

func TestRouteInstallationListingFlagsInvalidRecord(t *testing.T) {
	file, _, _, _ := redirectTestSetup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TFSAZ_CONFIG", file)
	system := func(context.Context, bool, string, string, string, string, string, string, []string) error {
		return nil
	}
	if err := runManagedRoute(t.Context(), []string{"setup"}, io.Discard, system); err != nil {
		t.Fatal(err)
	}
	id := routeSystemID(file)
	recovery := filepath.Join(home, ".tfsaz", "route-recovery")
	// A record naming another configuration must not redirect restore to it.
	other, err := json.Marshal(routeConfigRecord{ConfigFile: filepath.Join(t.TempDir(), "config.json")})
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, filepath.Join(recovery, id+routeConfigRecordSuffix), string(other))
	t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	var out strings.Builder
	if err := runManagedRoute(t.Context(), []string{"restore"}, &out, system); err != nil || !strings.Contains(out.String(), "invalid configuration record") {
		t.Fatalf("listing with an invalid record: %v\n%s", err, out.String())
	}
	if err := runManagedRoute(t.Context(), []string{"restore", "--installation", id}, io.Discard, system); err == nil || !strings.Contains(err.Error(), "invalid configuration record") {
		t.Fatalf("restore with an invalid record: %v", err)
	}
	if _, err := os.Stat(file + ".browser-route"); err != nil {
		t.Fatalf("route directory after a refused restore: %v", err)
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
	system := func(context.Context, bool, string, string, string, string, string, string, []string) error {
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
	system := func(context.Context, bool, string, string, string, string, string, string, []string) error {
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
			func(context.Context, bool, string, string, string, string, string, string, []string) error {
				return nil
			})
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
	system := func(_ context.Context, install bool, _, _, _, _, _, sha string, aliases []string) error {
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
