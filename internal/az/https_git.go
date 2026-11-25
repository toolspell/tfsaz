package az

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func gitHTTPSHelperName() string {
	if runtime.GOOS == "windows" {
		return "git-remote-https.exe"
	}
	return "git-remote-https"
}

// RunHTTPSGitHelper preserves Git's remote-helper protocol. Only the URL seen
// by the vendor HTTPS helper changes; Git retains the saved Azure-style remote.
func RunHTTPSGitHelper(ctx context.Context, args []string) error {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		return errors.New("expected a Git HTTPS remote name and URL")
	}
	core := os.Getenv("TFSAZ_GIT_CORE")
	if !filepath.IsAbs(core) {
		return errors.New("TFSAZ_GIT_CORE must be the absolute path to Git's original exec directory")
	}
	helper := filepath.Join(core, gitHTTPSHelperName())
	info, err := os.Stat(helper)
	if err != nil {
		return fmt.Errorf("find Git's original HTTPS helper: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("Git's original HTTPS helper must be a regular file")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("identify the running Git HTTPS helper: %w", err)
	}
	// The route launcher can install a copy when it crosses volumes.
	if sameBinary(helper, executable) {
		return errors.New("TFSAZ_GIT_CORE points to tfsaz itself; specify Git's original exec directory")
	}
	destination, err := gitHTTPSDestination(args[1])
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, helper, args[0], destination)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func gitAzureAlias(raw string) (azure, managed bool) {
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || !strings.EqualFold(scheme, "https") {
		return false, false
	}
	authority, path, _ := strings.Cut(rest, "/")
	if end := strings.IndexAny(authority, "?#"); end >= 0 {
		authority = authority[:end]
		path = ""
	}
	// Identify the authority independently of path parsing so that malformed
	// escape sequences in an alias path cannot cause a native Git fallback.
	u, err := url.Parse("https://" + authority)
	var hostname string
	if err != nil {
		// Git's URL parser may accept host spellings that net/url rejects.
		// Keep an alias-like authority in the validation path in either case.
		hostname, _, _ = strings.Cut(authority[strings.LastIndex(authority, "@")+1:], ":")
		if decoded, err := url.PathUnescape(hostname); err == nil {
			hostname = decoded
		}
	} else {
		hostname = u.Hostname()
	}
	host := strings.TrimSuffix(strings.ToLower(hostname), ".")
	if legacyHost.MatchString(host) {
		return true, strings.HasPrefix(host, "tfsaz-")
	}
	if host == "dev.azure.com" {
		first, _, _ := strings.Cut(path, "/")
		if end := strings.IndexAny(first, "?#"); end >= 0 {
			first = first[:end]
		}
		if value, err := url.PathUnescape(first); err == nil {
			first = value
		}
		return true, strings.HasPrefix(strings.ToLower(first), "tfsaz-")
	}
	return false, false
}

func gitHTTPSDestination(raw string) (string, error) {
	azure, managed := gitAzureAlias(raw)
	if !azure {
		return raw, nil
	}
	cfg, err := loadConfig()
	if errors.Is(err, os.ErrNotExist) {
		if managed {
			return "", errors.New("no active setup for the managed Git HTTPS alias")
		}
		return raw, nil
	}
	if err != nil {
		return "", err
	}
	var records []setupRecord
	if managed {
		records, err = readSetupRecords()
		if err != nil {
			return "", err
		}
	}
	route, err := mapHTTPSGitBase(cfg, records, raw)
	if errors.Is(err, errNoHTTPSRoute) {
		if managed {
			return "", errors.New("no active setup for the managed Git HTTPS alias")
		}
		return raw, nil
	}
	if err != nil {
		return "", err
	}
	return route.Destination, nil
}
