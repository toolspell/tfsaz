package main

import (
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
	if err := requireMac(); err != nil {
		return err
	}
	o, err := lookupOS(osName)
	if err != nil {
		return err
	}
	if err := e.ensureHome(); err != nil {
		return err
	}
	t := e.tart()
	clones, err := t.clones(o)
	if err != nil {
		return err
	}
	if len(clones) > 0 {
		return fmt.Errorf("%s has clones: %s; remove them first (vm down NAME or clean)", o.base(), strings.Join(clones, ", "))
	}
	switch action {
	case "build":
		return e.buildBase(o, force)
	case "update":
		return e.updateBase(o)
	case "rm":
		step("deleting %s", o.base())
		if err := t.remove(o.base()); err != nil {
			return err
		}
		return os.RemoveAll(e.path("keys", o.name+"-base"))
	}
	return errUsage
}

func (e *env) baseKey(o osDef) string { return e.path("keys", o.name+"-base", "id_ed25519") }

// buildBase installs the OS from its ISO into a build VM and replaces the
// base VM with it only when the build succeeded.
func (e *env) buildBase(o osDef, force bool) error {
	t := e.tart()
	exists, err := t.exists(o.base())
	if err != nil {
		return err
	}
	if exists && !force {
		return fmt.Errorf("%s exists; use base update %s, or base build %s -force to rebuild it", o.base(), o.name, o.name)
	}
	dir := filepath.Join(e.bed, "vms", o.name)
	iso, err := e.ensureISO(dir)
	if err != nil {
		return err
	}
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
	seed, err := e.seedISO(o, dir, key+".pub")
	if err != nil {
		return err
	}
	if err := t.remove(o.build()); err != nil {
		return err
	}
	packerEnv := append(t.env(),
		"PACKER_PLUGIN_PATH="+e.path("packer", "plugins"),
		"PACKER_CACHE_DIR="+e.path("packer", "cache"),
		"CHECKPOINT_DISABLE=1",
	)
	step("installing Packer plugins")
	if err := cmd(dir, packerEnv, "packer", "init", "."); err != nil {
		return err
	}
	step("building %s from %s (this takes a while)", o.build(), filepath.Base(iso))
	err = cmd(dir, packerEnv, "packer", "build", "-color=false",
		"-var", "vm_name="+o.build(), "-var", "iso="+iso, "-var", "seed_iso="+seed,
		"-var", "ssh_private_key_file="+key, ".")
	if err != nil {
		_ = t.remove(o.build())
		return fmt.Errorf("packer build failed: %w", err)
	}
	if err := t.remove(o.base()); err != nil {
		return err
	}
	if _, err := t.run("rename", o.build(), o.base()); err != nil {
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

// ensureISO downloads the installer named in iso.json into the cache and
// checks its SHA-256, reusing a cached copy that matches.
func (e *env) ensureISO(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "iso.json"))
	if err != nil {
		return "", err
	}
	var spec struct{ URL, SHA256 string }
	if err := json.Unmarshal(data, &spec); err != nil || spec.URL == "" || len(spec.SHA256) != 64 {
		return "", fmt.Errorf("%s/iso.json needs url and sha256", dir)
	}
	cache := e.path("cache", "iso")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	file := filepath.Join(cache, path.Base(spec.URL))
	if verified(file, spec.SHA256) {
		return file, nil
	}
	// curl -C - resumes a partial file left by an interrupted run.
	part := file + ".part"
	step("downloading %s", spec.URL)
	if err := cmd("", nil, "curl", "-fL", "--retry", "3", "--progress-bar", "-C", "-", "-o", part, spec.URL); err != nil {
		return "", fmt.Errorf("download %s: %w; run base build again to resume it", spec.URL, err)
	}
	if !verified(part, spec.SHA256) {
		_ = os.Remove(part)
		return "", fmt.Errorf("%s does not match the SHA-256 in %s/iso.json; deleted it", spec.URL, dir)
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

// seedISO writes the autoinstall config with this build's public key to an
// ISO labeled cidata, where the installer looks for it.
func (e *env) seedISO(o osDef, dir, publicKey string) (string, error) {
	tmpl, err := os.ReadFile(filepath.Join(dir, "user-data"))
	if err != nil {
		return "", err
	}
	pub, err := os.ReadFile(publicKey)
	if err != nil {
		return "", err
	}
	userData := strings.ReplaceAll(string(tmpl), "${ssh_public_key}", strings.TrimSpace(string(pub)))
	work := e.path("build", o.name)
	if err := os.RemoveAll(work); err != nil {
		return "", err
	}
	cidata := filepath.Join(work, "cidata")
	if err := os.MkdirAll(cidata, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(cidata, "user-data"), []byte(userData), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(cidata, "meta-data"), nil, 0o600); err != nil {
		return "", err
	}
	seed := filepath.Join(work, "seed.iso")
	_, err = output(nil, "hdiutil", "makehybrid", "-quiet", "-o", seed, cidata, "-iso", "-joliet", "-default-volume-name", "cidata")
	return seed, err
}

// updateBase boots the base VM, upgrades its packages and prepares it for
// cloning again.
func (e *env) updateBase(o osDef) error {
	t := e.tart()
	if ok, err := t.exists(o.base()); err != nil || !ok {
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
	if err := t.start(o.base(), filepath.Join(work, "update.log")); err != nil {
		return err
	}
	ip, err := t.ip(o.base(), 3*time.Minute)
	if err != nil {
		return errors.Join(err, t.stop(o.base()))
	}
	known := filepath.Join(work, "known_hosts")
	_ = os.Remove(known)
	login := sshTarget{user: o.user, host: ip, key: e.baseKey(o), knownHosts: known}
	if err := login.wait(3 * time.Minute); err != nil {
		return errors.Join(err, t.stop(o.base()))
	}
	step("upgrading packages")
	if _, err := login.run(nil, "sudo DEBIAN_FRONTEND=noninteractive sh -c 'apt-get update && apt-get -y -o DPkg::Lock::Timeout=600 full-upgrade && apt-get -y autoremove'"); err != nil {
		return errors.Join(err, e.shutdownBase(o, login))
	}
	if _, err := login.run(prepare, "sudo sh -eu -s"); err != nil {
		return errors.Join(err, e.shutdownBase(o, login))
	}
	step("updated %s", o.base())
	if err := e.shutdownBase(o, login); err != nil {
		// prepare-base.sh ended with sync, so a stop now loses nothing.
		return t.stop(o.base())
	}
	return nil
}

// shutdownBase shuts the base down from inside and waits for it. A base
// that keeps running is left running: a stop without a shutdown can lose
// writes, and every clone copies the base.
func (e *env) shutdownBase(o osDef, login sshTarget) error {
	_, _ = login.run(nil, "sudo systemctl poweroff")
	if err := e.tart().waitStopped(o.base(), 2*time.Minute); err != nil {
		return fmt.Errorf("%w; check it, then stop it with TART_HOME=%s tart stop %s", err, e.tart().home, o.base())
	}
	return nil
}
