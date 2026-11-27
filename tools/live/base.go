package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func (e *env) base(action, osName string, force bool) error {
	switch action {
	case "build", "update", "rm":
	default:
		return fmt.Errorf("unknown base action %q", action)
	}
	if err := requireMac(); err != nil {
		return err
	}
	o, err := lookupOS(osName)
	if err != nil {
		return err
	}
	unlock, err := acquireLock(e.path("bases.lock"), false)
	if err != nil {
		return err
	}
	defer unlock()
	return e.changeBase(o, action, force)
}

func baseCloneError(o osDef, clones []string) error {
	var commands []string
	for _, name := range clones {
		if _, legacy := legacyCloneOS(name); legacy {
			commands = append(commands, "go run ./tools/live vm down "+name)
		}
	}
	hint := "remove instance clones in their owning worktrees"
	if len(commands) != 0 {
		hint += "; remove legacy clones with " + strings.Join(commands, " or ")
	}
	return fmt.Errorf("%s has clones: %s; %s", o.base(), strings.Join(clones, ", "), hint)
}

func (e *env) changeBase(o osDef, action string, force bool) error {
	v := e.vms(o)
	clones, err := v.clones(o)
	if err != nil {
		return err
	}
	if len(clones) > 0 {
		return baseCloneError(o, clones)
	}
	if action == "build" || action == "update" {
		unlock, err := e.lockMacOSBoot(o)
		if err != nil {
			return err
		}
		defer unlock()
		if err := e.macosSlot(o); err != nil {
			return err
		}
	}
	switch action {
	case "build":
		return e.buildBase(o, force)
	case "update":
		return e.updateBase(o)
	}
	step("deleting %s", o.base())
	if err := v.remove(o.base()); err != nil {
		return err
	}
	return os.RemoveAll(e.path("keys", o.name+"-base"))
}

// baseHint says how to renew an existing base: Windows has no update in
// place.
func baseHint(o osDef) string {
	if o.guest.update == "" {
		return fmt.Sprintf("base build %s -force rebuilds it", o.name)
	}
	return fmt.Sprintf("base update %s upgrades it, base build %s -force rebuilds it", o.name, o.name)
}

func (e *env) baseKey(o osDef) string { return e.path("keys", o.name+"-base", "id_ed25519") }

// buildBase installs the OS into a build VM and replaces the base VM with it
// only when the build succeeded.
func (e *env) buildBase(o osDef, force bool) error {
	v := e.vms(o)
	exists, err := v.exists(o.base())
	if err != nil {
		return err
	}
	if exists && !force {
		return fmt.Errorf("%s exists; %s", o.base(), baseHint(o))
	}
	dir := filepath.Join(e.bed, "vms", o.name)
	// The new key replaces the base's key only with the base itself, so a
	// failed rebuild leaves the old base usable.
	key := e.path("keys", o.name+"-build", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(key)) }()
	if err := newSSHKey(key, "tfsaz-live "+o.base()); err != nil {
		return err
	}
	// The build directory holds the seed ISO, for Windows with the test
	// user's password; only the copy beside the base key stays.
	defer func() { _ = os.RemoveAll(e.path("build", o.name)) }()
	inputs, from, err := e.buildInputs(o, dir, key)
	if err != nil {
		return err
	}
	if err := v.remove(o.build()); err != nil {
		return err
	}
	packerEnv := append(e.tart().env(),
		"PACKER_PLUGIN_PATH="+e.path("packer", "plugins"),
		"PACKER_CACHE_DIR="+e.path("packer", "cache"),
		"CHECKPOINT_DISABLE=1",
	)
	step("installing Packer plugins")
	if err := cmd(dir, packerEnv, "packer", "init", "."); err != nil {
		return err
	}
	step("building %s from %s (this takes a while)", o.build(), from)
	args := []string{"build", "-color=false", "-var", "vm_name=" + o.build(), "-var", "ssh_private_key_file=" + key}
	for _, v := range inputs {
		args = append(args, "-var", v)
	}
	err = cmd(dir, packerEnv, "packer", append(args, ".")...)
	if err != nil {
		_ = v.remove(o.build())
		return fmt.Errorf("packer build failed: %w", err)
	}
	if err := v.remove(o.base()); err != nil {
		return err
	}
	if err := v.rename(o.build(), o.base()); err != nil {
		return err
	}
	baseKeys := filepath.Dir(e.baseKey(o))
	if err := os.RemoveAll(baseKeys); err != nil {
		return err
	}
	if err := os.Rename(filepath.Dir(key), baseKeys); err != nil {
		return err
	}
	step("built %s", o.base())
	return nil
}

// buildInputs returns the Packer variables of o's template besides the VM
// name and the build key, and what the build starts from.
func (e *env) buildInputs(o osDef, dir, key string) ([]string, string, error) {
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return nil, "", err
	}
	publicKey := strings.TrimSpace(string(pub))
	switch o.name {
	case "ubuntu":
		iso, err := e.ensureISO(dir, "iso.json")
		if err != nil {
			return nil, "", err
		}
		userData, err := render(filepath.Join(dir, "user-data"), map[string]string{"ssh_public_key": publicKey})
		if err != nil {
			return nil, "", err
		}
		seed, err := e.seedISO(o, "cidata", map[string]string{"user-data": userData, "meta-data": ""})
		return []string{"iso=" + iso, "seed_iso=" + seed}, filepath.Base(iso), err
	case "macos":
		data, err := os.ReadFile(filepath.Join(dir, "image.json"))
		if err != nil {
			return nil, "", err
		}
		var spec struct{ Image string }
		if err := json.Unmarshal(data, &spec); err != nil || !strings.Contains(spec.Image, "@sha256:") {
			return nil, "", fmt.Errorf("%s/image.json needs an image pinned by digest", dir)
		}
		// Tart pulls the image into its cache once; later builds reuse it.
		return []string{"base_image=" + spec.Image, "ssh_public_key=" + publicKey}, spec.Image, nil
	case "windows":
		return e.windowsInputs(o, dir, key, publicKey)
	}
	return nil, "", fmt.Errorf("%s has no base build", o.name)
}

// windowsInputs prepares Windows Setup: Microsoft's ISO, the virtio-win ISO
// with the network driver, and a seed ISO with Autounattend.xml and the
// first-logon script. The test user's password stays beside the base key.
func (e *env) windowsInputs(o osDef, dir, key, publicKey string) ([]string, string, error) {
	iso, err := e.ensureISO(dir, "iso.json")
	if err != nil {
		return nil, "", err
	}
	virtio, err := e.ensureISO(dir, "virtio.json")
	if err != nil {
		return nil, "", err
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	password := hex.EncodeToString(b)
	if err := os.WriteFile(filepath.Join(filepath.Dir(key), "password"), []byte(password+"\n"), 0o600); err != nil {
		return nil, "", err
	}
	answers, err := render(filepath.Join(dir, "Autounattend.xml"), map[string]string{"password": password})
	if err != nil {
		return nil, "", err
	}
	setup, err := render(filepath.Join(dir, "setup.ps1"), map[string]string{"ssh_public_key": publicKey})
	if err != nil {
		return nil, "", err
	}
	seed, err := e.seedISO(o, "SEED", map[string]string{"Autounattend.xml": answers, "setup.ps1": setup})
	if err != nil {
		return nil, "", err
	}
	code, vars, err := qemuFirmware()
	if err != nil {
		return nil, "", err
	}
	return []string{
		"iso=" + iso, "virtio_iso=" + virtio, "seed_iso=" + seed,
		"efi_code=" + code, "efi_vars=" + vars,
		"output_dir=" + e.qemu().dir(o.build()), "qmp_socket=" + e.path("build", o.name, "qmp.sock"),
	}, filepath.Base(iso), nil
}

// render reads a template and replaces each ${name} with its value.
func render(file string, values map[string]string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	text := string(data)
	for name, value := range values {
		text = strings.ReplaceAll(text, "${"+name+"}", value)
	}
	return text, nil
}

// ensureISO returns the installer image that spec, a JSON file in dir,
// names, from the cache. An image with a url is downloaded once; one with
// only a page must be downloaded by hand. Either must match its SHA-256.
func (e *env) ensureISO(dir, spec string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, spec))
	if err != nil {
		return "", err
	}
	var s struct{ URL, File, Page, SHA256 string }
	if err := json.Unmarshal(data, &s); err != nil || len(s.SHA256) != 64 || (s.URL == "") == (s.Page == "") || (s.URL == "" && s.File == "") {
		return "", fmt.Errorf("%s/%s needs sha256 and either url or page with file", dir, spec)
	}
	if s.File == "" {
		s.File = path.Base(s.URL)
	}
	cache := e.path("cache", "iso")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	file := filepath.Join(cache, filepath.Base(s.File))
	if verified(file, s.SHA256) {
		return file, nil
	}
	if s.URL == "" {
		if _, err := os.Stat(file); err == nil {
			return "", fmt.Errorf("%s does not match the SHA-256 in %s/%s", file, dir, spec)
		}
		return "", fmt.Errorf("%s is missing: download it from %s and save it as %s (its download links expire, so the tool cannot fetch it)", s.File, s.Page, file)
	}
	// curl -C - resumes a partial file left by an interrupted run.
	part := file + ".part"
	step("downloading %s", s.URL)
	if err := cmd("", nil, "curl", "-fL", "--retry", "3", "--progress-bar", "-C", "-", "-o", part, s.URL); err != nil {
		return "", fmt.Errorf("download %s: %w; run base build again to resume it", s.URL, err)
	}
	if !verified(part, s.SHA256) {
		_ = os.Remove(part)
		return "", fmt.Errorf("%s does not match the SHA-256 in %s/%s; deleted it", s.URL, dir, spec)
	}
	return file, os.Rename(part, file)
}

func verified(file, want string) bool {
	sum, err := fileSHA256(file)
	return err == nil && sum == want
}

func fileSHA256(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// seedISO writes files to an ISO with the volume name label in o's build
// directory, where the installer looks for its answers.
func (e *env) seedISO(o osDef, label string, files map[string]string) (string, error) {
	work := e.path("build", o.name)
	if err := os.RemoveAll(work); err != nil {
		return "", err
	}
	root := filepath.Join(work, "seed")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			return "", err
		}
	}
	seed := filepath.Join(work, "seed.iso")
	_, err := output(nil, "hdiutil", "makehybrid", "-quiet", "-o", seed, root, "-iso", "-joliet", "-default-volume-name", label)
	return seed, err
}

// updateBase boots the base VM, upgrades its packages and prepares it for
// cloning again.
func (e *env) updateBase(o osDef) error {
	if o.guest.update == "" {
		return fmt.Errorf("%s has no update in place; base build %s -force rebuilds it from %s", o.base(), o.name, filepath.Join("live-tests", "vms", o.name))
	}
	v := e.vms(o)
	if ok, err := v.exists(o.base()); err != nil || !ok {
		return errors.Join(err, fmt.Errorf("%s does not exist; run base build %s", o.base(), o.name))
	}
	prepare, err := os.Open(filepath.Join(e.bed, "vms", o.name, "prepare-base.sh"))
	if err != nil {
		return err
	}
	defer func() { _ = prepare.Close() }()
	work := e.path("build", o.name)
	if err := os.MkdirAll(work, 0o700); err != nil {
		return err
	}
	step("booting %s", o.base())
	if err := v.start(o.base(), filepath.Join(work, "update.log")); err != nil {
		return err
	}
	host, port, err := v.address(o.base(), 3*time.Minute)
	if err != nil {
		return errors.Join(err, v.stop(o.base()))
	}
	known := filepath.Join(work, "known_hosts")
	_ = os.Remove(known)
	login := sshTarget{user: o.user, host: host, port: port, key: e.baseKey(o), knownHosts: known}
	if err := login.wait(3 * time.Minute); err != nil {
		return errors.Join(err, v.stop(o.base()))
	}
	step("upgrading packages")
	if _, err := login.run(nil, o.guest.update); err != nil {
		return errors.Join(err, e.shutdownBase(o, login))
	}
	if _, err := login.run(prepare, "sudo sh -eu -s"); err != nil {
		return errors.Join(err, e.shutdownBase(o, login))
	}
	step("updated %s", o.base())
	if err := e.shutdownBase(o, login); err != nil {
		// prepare-base.sh ended with sync, so a stop now loses nothing.
		return v.stop(o.base())
	}
	return nil
}

// shutdownBase shuts the base down from inside and waits for it. A base
// that keeps running is left running: a stop without a shutdown can lose
// writes, and every clone copies the base.
func (e *env) shutdownBase(o osDef, login sshTarget) error {
	// A guest that shuts down may never end the session; the wait below
	// tells whether it stopped. Only Tart bases have an update in place.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = login.runContext(ctx, nil, o.guest.poweroff)
	if err := e.vms(o).waitStopped(o.base(), 2*time.Minute); err != nil {
		return fmt.Errorf("%w; check it, then stop it with TART_HOME=%s tart stop %s", err, e.tart().home, o.base())
	}
	return nil
}
