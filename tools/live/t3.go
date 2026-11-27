package main

import (
	"bufio"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const t3Releases = "https://github.com/pingdotgg/t3code/releases/download/"

// t3PinPattern finds the T3 Code tag that tools/syncrepos pins; live tests
// install the same release, so they test what the reference code shows.
var t3PinPattern = regexp.MustCompile(`name:\s*"t3code",[^}]*?tag:\s*"([^"]+)"`)

func (e *env) t3Pin() (string, error) {
	data, err := os.ReadFile(filepath.Join(e.root, "tools", "syncrepos", "main.go"))
	if err != nil {
		return "", err
	}
	m := t3PinPattern.FindSubmatch(data)
	if m == nil {
		return "", fmt.Errorf("no t3code tag in tools/syncrepos/main.go")
	}
	return string(m[1]), nil
}

// t3Installer names the arm64 desktop installer of a release for a guest OS
// and the electron-builder manifest that holds its SHA-512.
func t3Installer(osName, tag string) (manifest, file string, err error) {
	version := strings.TrimPrefix(tag, "v")
	channel := "latest"
	if strings.Contains(version, "nightly") {
		channel = "nightly"
	}
	switch osName {
	case "macos":
		return channel + "-mac.yml", "T3-Code-" + version + "-arm64.zip", nil
	case "ubuntu":
		return channel + "-linux-arm64.yml", "T3-Code-" + version + "-arm64.deb", nil
	case "windows":
		return channel + ".yml", "T3-Code-" + version + "-arm64.exe", nil
	}
	return "", "", fmt.Errorf("no T3 installer for %s", osName)
}

// manifestSHA512 reads the SHA-512 of file from an electron-builder manifest.
func manifestSHA512(manifest io.Reader, file string) (string, error) {
	s := bufio.NewScanner(manifest)
	found := false
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if url, ok := strings.CutPrefix(line, "- url: "); ok {
			found = url == file
			continue
		}
		if sum, ok := strings.CutPrefix(line, "sha512: "); ok && found {
			return sum, nil
		}
	}
	return "", fmt.Errorf("the manifest lists no SHA-512 for %s", file)
}

// ensureT3 downloads the pinned T3 installer for osName once and checks it
// against its release manifest.
func (e *env) ensureT3(osName string) (tag, path string, err error) {
	if tag, err = e.t3Pin(); err != nil {
		return "", "", err
	}
	manifest, file, err := t3Installer(osName, tag)
	if err != nil {
		return "", "", err
	}
	dir := e.path("cache", "t3", tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	manifestPath := filepath.Join(dir, manifest)
	if _, err := os.Stat(manifestPath); err != nil {
		if err := download(manifestPath, t3Releases+tag+"/"+manifest, ""); err != nil {
			return "", "", err
		}
	}
	m, err := os.Open(manifestPath)
	if err != nil {
		return "", "", err
	}
	want, err := manifestSHA512(m, file)
	_ = m.Close()
	if err != nil {
		return "", "", err
	}
	path = filepath.Join(dir, file)
	if ok, _ := hasSHA512(path, want); ok {
		return tag, path, nil
	}
	step("downloading %s", file)
	if err := download(path, t3Releases+tag+"/"+file, want); err != nil {
		return "", "", fmt.Errorf("%s: %w", file, err)
	}
	return tag, path, nil
}

// download fetches url to path through a temporary file of its own, so a
// failed download leaves nothing behind and parallel runs, for example
// from other worktrees, do not write the same file. A file whose SHA-512
// is not want (when given) is deleted.
func download(path, url, want string) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	part := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(part) }()
	if err := cmd("", nil, "curl", "-fL", "--retry", "3", "--progress-bar", "-o", part, url); err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if want != "" {
		if ok, err := hasSHA512(part, want); !ok {
			return fmt.Errorf("the download does not match the SHA-512 of the release manifest; deleted it (%v)", err)
		}
	}
	if err := os.Rename(part, path); err != nil {
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			if want == "" {
				return nil
			}
			if ok, _ := hasSHA512(path, want); ok {
				return nil
			}
		}
		return err
	}
	return nil
}

func hasSHA512(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)) == want, nil
}

// t3Name is the product name of a T3 Code release: the app's name on macOS
// and its executable's on Windows.
func t3Name(tag string) string {
	if strings.Contains(tag, "nightly") {
		return "T3 Code (Nightly)"
	}
	return "T3 Code"
}

// vmT3 runs vm t3 install and vm t3 start.
func (e *env) vmT3(args []string) error {
	if len(args) < 2 {
		return errUsage
	}
	switch args[0] {
	case "install":
		if len(args) != 2 {
			return errUsage
		}
		return e.vmT3Install(args[1])
	case "start":
		flags := flag.NewFlagSet("vm t3 start", flag.ContinueOnError)
		port := flags.Int("port", 9222, "Chrome debugging port of T3 in the clone")
		if flags.Parse(args[2:]) != nil || flags.NArg() != 0 || *port < 1 || *port > 65535 {
			return errUsage
		}
		return e.vmT3Start(args[1], *port)
	}
	return errUsage
}

// vmT3Install installs the stock T3 Code desktop app of the reference pin
// into a clone, as its user would: the clones start without it, so a test
// can also cover a machine without T3.
func (e *env) vmT3Install(name string) error {
	info, err := e.loadVM(name)
	if err != nil {
		return err
	}
	o, _ := osOfVM(name)
	tag, local, err := e.ensureT3(o.name)
	if err != nil {
		return err
	}
	login := info.login()
	remote := o.guest.tempDir + filepath.Base(local)
	step("installing T3 Code %s in %s", tag, name)
	if _, err := output(nil, "scp", login.scpArgs(local, remote, true)...); err != nil {
		return err
	}
	if _, err := login.run(nil, fmt.Sprintf(o.guest.t3Install, remote)); err != nil {
		return err
	}
	fmt.Println(tag)
	return nil
}

// vmT3Start starts the installed T3 Code in the clone's desktop session with
// Chrome's debugging port on the clone's loopback, waits until the port
// answers and prints its URL in the clone. Playwright attaches to it through
// vm forward.
func (e *env) vmT3Start(name string, port int) error {
	info, err := e.loadVM(name)
	if err != nil {
		return err
	}
	o, _ := osOfVM(name)
	tag, err := e.t3Pin()
	if err != nil {
		return err
	}
	remote, err := guiRemote(o, fmt.Sprintf(o.guest.t3Start, t3Name(tag), port), time.Minute)
	if err != nil {
		return err
	}
	login := info.login()
	step("starting T3 Code in %s", name)
	// Windows enforces the -gui timeout in the guest; the host only catches
	// a guest that stops answering.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute+30*time.Second)
	defer cancel()
	if _, err := login.runRemote(ctx, nil, remote, "vm t3 start"); err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(time.Minute)
	for {
		if _, err = login.run(nil, fmt.Sprintf(o.guest.answers, url+"/json/version")); err == nil {
			fmt.Println(url)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("T3's debugging port %d in %s did not answer within a minute; a T3 that already ran without it keeps running, so quit it first: %w", port, name, err)
		}
		time.Sleep(2 * time.Second)
	}
}
