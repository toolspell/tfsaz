package main

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLiveHelp(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CI", "true")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("TFSAZ_LIVE_HOME", "relative")
	type helpCase struct {
		path, synopsis string
		flags          []string
	}
	cases := []helpCase{
		{"", "<command>", nil},
		{"up", " [OS...]", nil},
		{"down", "", nil},
		{"prune", "", nil},
		{"setup", " OS", nil},
		{"verify", " OS", nil},
		{"doctor", " [-install] [-yes]", []string{"-install", "-yes"}},
		{"services", " <action>", nil},
		{"services up", "", nil},
		{"services down", "", nil},
		{"services status", " [-json]", []string{"-json"}},
		{"mock", " <action>", nil},
		{"mock ca", "", nil},
		{"mock requests", " [-vm NAME] [-json]", []string{"-vm", "-json"}},
		{"mock reset", "", nil},
		{"base", " <action>", nil},
		{"base build", " OS [-force]", []string{"-force"}},
		{"base update", " OS", nil},
		{"base rm", " OS", nil},
		{"vm", " <action>", nil},
		{"vm up", " OS [-name SUFFIX]", []string{"-name"}},
		{"vm ssh", " NAME [-- CMD...]", nil},
		{"vm run", " NAME [-json] [-timeout D] [-gui] -- CMD...", []string{"-json", "-timeout", "-gui"}},
		{"vm push", " NAME LOCAL REMOTE", nil},
		{"vm pull", " NAME REMOTE LOCAL", nil},
		{"vm forward", " NAME PORT[:PORT]", nil},
		{"vm screenshot", " NAME FILE", nil},
		{"vm vnc", " NAME", nil},
		{"vm t3", " <action>", nil},
		{"vm t3 install", " NAME", nil},
		{"vm t3 start", " NAME [-port P]", []string{"-port"}},
		{"vm browser", " <action>", nil},
		{"vm browser start", " NAME BROWSER [-port P] [URL]", []string{"-port"}},
		{"vm stop", " NAME [-force]", []string{"-force"}},
		{"vm start", " NAME", nil},
		{"vm down", " NAME", nil},
		{"vm list", " [-json]", []string{"-json"}},
		{"clean", "", nil},
		{"status", "", nil},
	}
	for _, h := range liveCommands {
		if !slices.ContainsFunc(cases, func(tc helpCase) bool { return tc.path == h.path }) {
			t.Errorf("missing help and lock policy case for %q", h.path)
		}
	}
	locked := []string{"up", "down", "clean", "setup", "verify", "services up", "services down",
		"mock reset", "vm up", "vm start", "vm stop", "vm down"}
	for _, tc := range cases {
		t.Run("lock policy/"+tc.path, func(t *testing.T) {
			command, action, _ := strings.Cut(tc.path, " ")
			if got, want := locksInstance(command, action), slices.Contains(locked, tc.path); got != want {
				t.Errorf("locksInstance(%s) = %t; want %t", tc.path, got, want)
			}
		})
		path := strings.Fields(tc.path)
		synopsis := strings.TrimSpace(tc.path + tc.synopsis)
		for _, args := range [][]string{
			append([]string{"help"}, path...),
			append(strings.Fields(tc.path), "-h"),
			append(strings.Fields(tc.path), "--help"),
		} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				var stdout, stderr bytes.Buffer
				code := liveMain(args, &stdout, &stderr)
				header, body, _ := strings.Cut(stdout.String(), "\n")
				if code != 0 || stderr.Len() != 0 || header != "usage: go run ./tools/live "+synopsis || strings.TrimSpace(body) == "" {
					t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
				}
				for _, name := range tc.flags {
					if !strings.Contains(body, "  "+name) {
						t.Errorf("missing %s flag description in %q", name, body)
					}
				}
				if (tc.path == "base update" || tc.path == "base rm") && strings.Contains(body, "-force") {
					t.Errorf("unexpected -force option in %q", body)
				}
			})
		}
	}
}

func TestLiveUsageErrors(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
	const name = "tfsaz-live-first-ubuntu-demo"
	for _, tc := range []struct{ args, path, message string }{
		{"", "<command>", "missing command"},
		{"ghost", "<command>", "unknown command"},
		{"help ghost", "<command>", "unknown command"},
		{"help vm t3 start extra", "vm t3 start NAME", "unexpected operand"},
		{"up -bad", "up [OS...]", "flag provided but not defined"},
		{"up ubuntu solaris", "up [OS...]", "unknown OS"},
		{"down extra", "down", "unexpected operand"},
		{"prune extra", "prune", "unexpected operand"},
		{"setup", "setup OS", "missing OS"},
		{"verify solaris", "verify OS", "unknown OS"},
		{"doctor extra", "doctor [-install]", "unexpected operand"},
		{"doctor -yes=maybe", "doctor [-install]", "invalid boolean value"},
		{"doctor -bad", "doctor [-install]", "flag provided but not defined"},
		{"services", "services <action>", "missing action"},
		{"services bogus", "services <action>", "unknown action"},
		{"services up extra", "services up", "unexpected operand"},
		{"services status -json extra", "services status [-json]", "unexpected operand"},
		{"mock bogus", "mock <action>", "unknown action"},
		{"mock ca extra", "mock ca", "unexpected operand"},
		{"mock requests -vm", "mock requests", "flag needs an argument"},
		{"mock requests -vm bogus", "mock requests", "invalid clone name"},
		{"mock reset extra", "mock reset", "unexpected operand"},
		{"base bogus ubuntu", "base <action>", "unknown action"},
		{"base build", "base build OS", "missing OS"},
		{"base build ubuntu extra", "base build OS", "unexpected operand"},
		{"base update windows", "base update OS", "base update supports ubuntu and macos"},
		{"base update ubuntu -force", "base update OS", "flag provided but not defined"},
		{"base rm ubuntu -force", "base rm OS", "flag provided but not defined"},
		{"vm", "vm <action>", "missing action"},
		{"vm unknown", "vm <action>", "unknown action"},
		{"vm up ubuntu -name -bad", "vm up OS", "invalid -name suffix"},
		{"vm up ubuntu -name up", "vm up OS", "reserved"},
		{"vm ssh " + name + " cmd", "vm ssh NAME", "unexpected operand"},
		{"vm run", "vm run NAME", "missing NAME"},
		{"vm run " + name, "vm run NAME", "missing -- CMD"},
		{"vm run " + name + " --", "vm run NAME", "missing CMD after --"},
		{"vm run " + name + " -timeout bad -- cmd", "vm run NAME", "invalid value"},
		{"vm run " + name + " extra -- cmd", "vm run NAME", "unexpected operand"},
		{"vm push " + name + " file", "vm push NAME", "missing REMOTE"},
		{"vm pull " + name + " remote local extra", "vm pull NAME", "unexpected operand"},
		{"vm forward " + name + " -1", "vm forward NAME", "invalid port"},
		{"vm forward " + name + " 0", "vm forward NAME", "invalid port"},
		{"vm forward " + name + " 9222:65536", "vm forward NAME", "invalid port"},
		{"vm forward " + name + " 9222:text", "vm forward NAME", "invalid port"},
		{"vm screenshot " + name, "vm screenshot NAME", "missing FILE"},
		{"vm vnc bad", "vm vnc NAME", "invalid clone name"},
		{"vm t3", "vm t3 <action>", "missing action"},
		{"vm t3 bogus", "vm t3 <action>", "unknown action"},
		{"vm t3 install", "vm t3 install NAME", "missing NAME"},
		{"vm t3 start " + name + " -port 0", "vm t3 start NAME", "invalid port"},
		{"vm t3 start " + name + " -port abc", "vm t3 start NAME", "invalid value"},
		{"vm browser", "vm browser <action>", "missing action"},
		{"vm browser start " + name, "vm browser start NAME BROWSER", "missing BROWSER"},
		{"vm browser start " + name + " chrome -port 65536", "vm browser start NAME BROWSER", "invalid port"},
		{"vm browser start " + name + " edge", "vm browser start NAME BROWSER", "only Windows clones come with Edge"},
		{"vm browser start " + name + " chrome about:blank extra", "vm browser start NAME BROWSER", "unexpected operand"},
		{"vm stop " + name + " -bad", "vm stop NAME", "flag provided but not defined"},
		{"vm start " + name + " extra", "vm start NAME", "unexpected operand"},
		{"vm down", "vm down NAME", "missing NAME"},
		{"vm list -json extra", "vm list [-json]", "unexpected operand"},
		{"clean extra", "clean", "unexpected operand"},
		{"status extra", "status", "unexpected operand"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := liveMain(strings.Fields(tc.args), &stdout, &stderr)
			diagnostic, usage, _ := strings.Cut(stderr.String(), "\n")
			if code != 2 || stdout.Len() != 0 || !strings.HasPrefix(diagnostic, "live: ") ||
				!strings.Contains(diagnostic, tc.message) || !strings.HasPrefix(usage, "usage: go run ./tools/live "+tc.path) ||
				strings.Count(usage, "\n") != 1 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestLiveGuestArgs(t *testing.T) {
	const name = "tfsaz-live-first-ubuntu-demo"
	for _, tc := range []struct {
		args, path, guest string
		asJSON, gui       bool
	}{
		{"vm run " + name + " -json -gui -- cmd --help", "vm run", "cmd --help", true, true},
		{"vm run " + name + " -- cmd -h -- extra", "vm run", "cmd -h -- extra", false, false},
		{"vm ssh " + name + " -- cmd --help", "vm ssh", "cmd --help", false, false},
		{"vm ssh " + name, "vm ssh", "", false, false},
	} {
		t.Run(tc.args, func(t *testing.T) {
			help, args, showHelp, err := resolveHelp(strings.Fields(tc.args))
			if help.path != tc.path || showHelp || err != nil {
				t.Fatalf("resolveHelp got %q, %v, %v", help.path, showHelp, err)
			}
			call := liveInvocation{commandHelp: help}
			if err := call.readArgs(args, call.flagSet()); err != nil {
				t.Fatal(err)
			}
			if call.args[0] != name || strings.Join(call.guest, " ") != tc.guest || call.asJSON != tc.asJSON || call.gui != tc.gui {
				t.Fatalf("parsed NAME %q, guest %q, -json %v, -gui %v", call.args[0], call.guest, call.asJSON, call.gui)
			}
		})
	}
}
