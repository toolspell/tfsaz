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

func (a app) runRoute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return writeHelp(a.stdout, "route")
	}
	switch args[0] {
	case "setup", "verify", "restore":
		return runManagedRoute(ctx, args, a.stdout, manageRouteSystem)
	case "handler-export":
		if len(args) != 3 {
			return usageError{errors.New("expected route handler-export DESTINATION BROWSER")}
		}
		return exportDesktopLinkHandler(ctx, args[1], args[2])
	case "handler-open":
		if len(args) != 4 {
			return usageError{errors.New("expected route handler-open CONFIG BROWSER URL")}
		}
		return openDesktopLink(ctx, args[1], args[2], args[3])
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
	if args[0] == "redirect" {
		// Managed macOS services append an internal flag to serve the socket
		// that launchd passes on standard input.
		opts := args[1:]
		inherited := len(opts) > 0 && opts[len(opts)-1] == routeInheritedSocketFlag
		if inherited {
			opts = opts[:len(opts)-1]
		}
		certFile, keyFile, port, err := parseRedirectOptions(opts)
		if err != nil {
			return err
		}
		if inherited {
			return serveRouteRedirectInherited(ctx, certFile, keyFile, port, a.stderr)
		}
		return serveRouteRedirect(ctx, certFile, keyFile, port, a.stderr)
	}
	hosts := args[0] == "hosts" || args[0] == "verify-hosts"
	if hosts && (len(args) > 2 || len(args) == 2 && args[1] != "--include-dev-azure") {
		return usageError{errors.New("expected an optional --include-dev-azure")}
	}
	proxy := args[0] == "proxy" || args[0] == "pac"
	if !hosts && !proxy && (len(args) != 2 || (args[0] != "resolve" && args[0] != "explain" && args[0] != "browser-export" && args[0] != "browser-verify")) {
		return usageError{errors.New("expected a route subcommand; see tfsaz route --help")}
	}
	var port int
	var shared bool
	var err error
	if proxy {
		port, shared, err = parseProxyOptions(args[1:])
		if err != nil {
			return err
		}
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
func (a app) runWithHTTPSGit(ctx context.Context, args []string) (err error) {
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
	// A T3 server may run longer than the OS temporary-file retention period,
	// so the overlay lives beside the configuration.
	file, err := configPath()
	if err != nil {
		return err
	}
	parent := filepath.Clean(file) + ".run"
	overlay, err := makeRouteRunOverlay(parent)
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.RemoveAll(overlay); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
		// Another run's overlay keeps the parent; its removal error says only that.
		_ = os.Remove(parent)
	}()
	shim := filepath.Join(overlay, helper)
	// Windows refuses to delete a hard link to an executable that is still
	// running, and this process runs until it removes the overlay. Hard links
	// also cross neither volumes nor filesystem boundaries. Copy in both cases.
	if runtime.GOOS == "windows" || os.Link(binary, shim) != nil {
		if err := copyGitSupportFile(binary, shim); err != nil {
			return err
		}
	}
	if err := populateGitExecPath(core, overlay, helper); err != nil {
		return err
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

// A run that ends concurrently may remove the empty parent between the
// MkdirAll and the MkdirTemp, so recreate it once.
func makeRouteRunOverlay(parent string) (string, error) {
	for attempt := 0; ; attempt++ {
		if err := os.MkdirAll(parent, 0700); err != nil {
			return "", err
		}
		overlay, err := os.MkdirTemp(parent, "git-")
		if err == nil || attempt > 0 || !errors.Is(err, os.ErrNotExist) {
			return overlay, err
		}
	}
}

// Git scripts source libraries relative to git --exec-path. PATH alone cannot
// supply them. Keep Windows binaries with their DLLs in the original directory.
func populateGitExecPath(core, overlay, helper string) error {
	entries, err := os.ReadDir(core)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == helper {
			continue
		}
		from, to := filepath.Join(core, name), filepath.Join(overlay, name)
		if runtime.GOOS != "windows" {
			if err := os.Symlink(from, to); err != nil {
				return err
			}
			continue
		}
		// A non-admin installation can copy the small scripts and data
		// directories even when symbolic links are unavailable.
		switch strings.ToLower(filepath.Ext(name)) {
		case ".exe", ".dll":
			continue
		}
		if entry.IsDir() {
			err = os.CopyFS(to, os.DirFS(from))
		} else {
			err = copyGitSupportFile(from, to)
		}
		if err != nil {
			return fmt.Errorf("prepare Git support file %s: %w", name, err)
		}
	}
	return nil
}

func copyGitSupportFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer closeQuietly(src)
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return errors.Join(err, dst.Close())
}
