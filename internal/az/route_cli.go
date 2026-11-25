package az

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/term"
)

const routeUsage = `tfsaz route explain URL
tfsaz route resolve URL
tfsaz route mac-handler-export NEW.app BROWSER_BUNDLE_ID
tfsaz route browser-target URL
tfsaz route verify-node
tfsaz route browser-export NEW_DIRECTORY
tfsaz route browser-verify DIRECTORY
tfsaz route run -- PROGRAM [ARG ...]
tfsaz route hosts [--include-dev-azure]
tfsaz route verify-hosts [--include-dev-azure]
tfsaz route sink [--port PORT]
tfsaz route proxy [--port PORT] [--include-dev-azure]
tfsaz route pac [--port PORT] [--include-dev-azure]`

func (a app) runRoute(ctx context.Context, args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		_, err := fmt.Fprintln(a.stdout, routeUsage)
		return err
	}
	switch args[0] {
	case "mac-handler-export":
		if len(args) != 3 {
			return usageError{errors.New("expected route mac-handler-export NEW.app BROWSER_BUNDLE_ID")}
		}
		return exportMacLinkHandler(ctx, args[1], args[2])
	case "browser-target":
		if len(args) != 2 {
			return usageError{errors.New("expected route browser-target URL")}
		}
		target, err := browserTarget(args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.stdout, target)
		return err
	case "verify-node":
		if len(args) != 1 {
			return usageError{errors.New("expected route verify-node")}
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return verifyRouteNode(ctx, cfg, a.stdout)
	}
	if args[0] == "run" && len(args) >= 3 && args[1] == "--" {
		return a.runWithHTTPSGit(ctx, args[2:])
	}
	if args[0] == "sink" {
		if len(args) == 1 {
			return runRouteSink(ctx, "443", a.stderr)
		}
		if len(args) == 3 && args[1] == "--port" {
			return runRouteSink(ctx, args[2], a.stderr)
		}
		return usageError{errors.New("expected route sink [--port PORT]")}
	}
	hosts := args[0] == "hosts" || args[0] == "verify-hosts"
	if hosts && (len(args) > 2 || len(args) == 2 && args[1] != "--include-dev-azure") {
		return usageError{errors.New("expected an optional --include-dev-azure")}
	}
	proxy := args[0] == "proxy" || args[0] == "pac"
	if !hosts && !proxy && (len(args) != 2 || (args[0] != "resolve" && args[0] != "explain" && args[0] != "browser-export" && args[0] != "browser-verify")) {
		return usageError{errors.New("expected a route subcommand; see tfsaz route --help")}
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	records, err := readSetupRecords()
	if err != nil {
		return err
	}
	if proxy {
		port, shared, err := parseProxyOptions(args[1:])
		if err != nil {
			return err
		}
		names, err := routeHostnames(cfg, records, shared)
		if err != nil {
			return err
		}
		if args[0] == "pac" {
			return writeRoutePAC(a.stdout, names, port)
		}
		return serveRouteProxy(ctx, names, port, a.stderr)
	}
	if hosts {
		return a.routeHosts(ctx, args[0], cfg, records, len(args) == 2)
	}
	if args[0] == "browser-verify" {
		return verifyHTTPSBrowserExtension(args[1], cfg, records)
	}
	if args[0] == "browser-export" {
		return writeHTTPSBrowserExtension(args[1], cfg, records)
	}
	route, err := mapHTTPSRoute(cfg, records, args[1])
	if err != nil {
		return err
	}
	if args[0] == "resolve" {
		_, err = fmt.Fprintln(a.stdout, route.Destination)
		return err
	}
	return json.NewEncoder(a.stdout).Encode(route)
}

// Only the child process tree sees the replacement HTTPS transport. The
// original exec directory stays in PATH so Git can find its other programs.
func (a app) runWithHTTPSGit(ctx context.Context, args []string) error {
	core := os.Getenv("TFSAZ_GIT_CORE")
	if core == "" {
		out, err := exec.CommandContext(ctx, "git", "--exec-path").Output()
		if err != nil {
			return errors.New("could not locate Git's HTTPS transport; check git on PATH")
		}
		core = strings.TrimRight(string(out), "\r\n")
	}
	helper := gitHTTPSHelperName()
	if info, err := os.Stat(filepath.Join(core, helper)); !filepath.IsAbs(core) || err != nil || !info.Mode().IsRegular() {
		return errors.New("Git's exec directory must contain its native HTTPS transport")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return err
	}
	// A T3 server may run longer than the OS temporary-file retention period.
	parent, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	parent = filepath.Join(parent, "tfsaz", "run")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	overlay, err := os.MkdirTemp(parent, "git-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(overlay)
	shim := filepath.Join(overlay, helper)
	if err := os.Link(binary, shim); err != nil {
		// Hard links can cross neither volumes nor filesystem boundaries.
		src, err := os.Open(binary)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.OpenFile(shim, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(dst, src)
		if err := errors.Join(writeErr, dst.Close()); err != nil {
			return err
		}
	}
	// Git shell tools also source files relative to git --exec-path. Preserve
	// the vendor tree on systems with ordinary unprivileged symlinks.
	if runtime.GOOS != "windows" {
		entries, err := os.ReadDir(core)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == helper {
				continue
			}
			if err := os.Symlink(filepath.Join(core, entry.Name()), filepath.Join(overlay, entry.Name())); err != nil {
				return err
			}
		}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = a.cwd
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.stdin, a.stdout, a.stderr
	cmd.Env = append(cmd.Environ(), "GIT_EXEC_PATH="+overlay, "TFSAZ_GIT_CORE="+core,
		"PATH="+overlay+string(os.PathListSeparator)+os.Getenv("PATH")+string(os.PathListSeparator)+core)
	if a.stdin == nil || !term.IsTerminal(int(a.stdin.Fd())) {
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	}
	return cmd.Run()
}
