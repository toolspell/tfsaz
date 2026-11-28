package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	imageRegistry       = "ghcr.io/toolspell/tfsaz-live-"
	imageLocalPrefix    = "tfsaz-live-local/"
	imageRepository     = "toolspell/tfsaz"
	imageSignerWorkflow = "toolspell/tfsaz/.github/workflows/testbed-images.yml"
	imageSourceRef      = "refs/heads/main"
	minGHVersion        = "2.102.0"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (img testbedImage) remote() string { return imageRegistry + img.name }

func (img testbedImage) localTag(key string) string {
	return imageLocalPrefix + img.name + ":" + imageTag(key)
}

// envVar names the Compose variable that receives the image reference.
func (img testbedImage) envVar() string {
	return "TFSAZ_LIVE_" + strings.ToUpper(img.name) + "_IMAGE"
}

// resolveImages picks the exact image each service runs and returns the
// Compose environment that names it: a verified ghcr.io digest when the host
// can use prebuilt images, otherwise a local build from the staged context.
func (e *env) resolveImages() ([]string, error) {
	engine, err := output(nil, "docker", "version", "--format", "{{.Server.Os}}/{{.Server.Arch}}")
	if err != nil {
		return nil, fmt.Errorf("ask Docker for its platform: %w; start Docker and retry", err)
	}
	r := imageResolver{e: e, arm64: engine == imagePlatform}
	if !r.arm64 {
		step("Docker runs %s, not %s: building the test bed images locally", engine, imagePlatform)
	}
	var vars []string
	for _, img := range testbedImages {
		ref, err := r.resolve(img)
		if err != nil {
			return nil, err
		}
		vars = append(vars, img.envVar()+"="+ref)
	}
	return vars, nil
}

type imageResolver struct {
	e     *env
	arm64 bool
	// gh is checked at most once, and only when a cache miss needs it.
	ghChecked, ghReady bool
}

func (r *imageResolver) resolve(img testbedImage) (string, error) {
	files, err := img.readContext(r.e.bed)
	if err != nil {
		return "", err
	}
	key := img.key(files)
	if r.arm64 {
		if digest, ok := r.e.cachedDigest(img, key); ok {
			ref := img.remote() + "@" + digest
			// A digest is immutable and passed verification when it was cached.
			if imagePresent(ref) {
				return ref, nil
			}
			if err := cmd("", nil, "docker", "pull", "--platform", imagePlatform, ref); err == nil {
				return ref, nil
			}
			step("could not pull %s; building it locally", ref)
		} else if r.prebuiltEligible() {
			if ref, ok := r.e.pullVerified(img, key); ok {
				return ref, nil
			}
		}
	}
	return buildLocal(img, key, files)
}

func (r *imageResolver) prebuiltEligible() bool {
	if !r.ghChecked {
		r.ghChecked = true
		var reason string
		r.ghReady, reason = ghStatus()
		if !r.ghReady {
			step("%s: building the test bed images locally", reason)
		}
	}
	return r.ghReady
}

// ghStatus reports whether gh can verify attestations, or why not.
func ghStatus() (bool, string) {
	out, err := output(nil, "gh", "--version")
	if err != nil {
		return false, "gh is missing or not working"
	}
	version := versionPattern.FindString(out)
	if version == "" || versionLess(version, minGHVersion) {
		return false, fmt.Sprintf("gh %s is older than %s", version, minGHVersion)
	}
	if _, err := output(nil, "gh", "auth", "status"); err != nil {
		return false, "gh is not signed in (gh auth login)"
	}
	return true, "gh " + version + ", signed in"
}

func imagePresent(ref string) bool {
	_, err := output(nil, "docker", "image", "inspect", "--format", "{{.Id}}", ref)
	return err == nil
}

// pullVerified pulls the key's tag and accepts its digest only when its
// provenance names this repository's image workflow on main and its key
// label matches. Otherwise it drops the pulled tag.
func (e *env) pullVerified(img testbedImage, key string) (string, bool) {
	tagRef := img.remote() + ":" + imageTag(key)
	step("pulling %s", tagRef)
	if err := cmd("", nil, "docker", "pull", "--platform", imagePlatform, tagRef); err != nil {
		step("no prebuilt %s image for these inputs; building it locally", img.name)
		return "", false
	}
	ref, err := verifyPulled(img, key, tagRef)
	if err != nil {
		step("rejected %s: %v; building it locally", tagRef, err)
		if _, err := output(nil, "docker", "image", "rm", tagRef); err != nil {
			step("could not remove %s: %v", tagRef, err)
		}
		return "", false
	}
	if err := e.cacheDigest(img, key, strings.TrimPrefix(ref, img.remote()+"@")); err != nil {
		step("could not cache the verified digest: %v", err)
	}
	return ref, true
}

func verifyPulled(img testbedImage, key, tagRef string) (string, error) {
	out, err := output(nil, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", tagRef)
	if err != nil {
		return "", err
	}
	var repoDigests []string
	if err := json.Unmarshal([]byte(out), &repoDigests); err != nil {
		return "", fmt.Errorf("read its digest: %w", err)
	}
	var ref string
	for _, d := range repoDigests {
		if digest, ok := strings.CutPrefix(d, img.remote()+"@"); ok && digestPattern.MatchString(digest) {
			ref = d
		}
	}
	if ref == "" {
		return "", fmt.Errorf("no %s digest among %q", img.remote(), repoDigests)
	}
	if _, err := output(nil, "gh", "attestation", "verify", "oci://"+ref,
		"--repo", imageRepository, "--signer-workflow", imageSignerWorkflow, "--source-ref", imageSourceRef); err != nil {
		return "", fmt.Errorf("provenance check failed: %w", err)
	}
	label, err := output(nil, "docker", "image", "inspect", "--format", `{{index .Config.Labels "`+imageKeyLabel+`"}}`, ref)
	if err != nil {
		return "", err
	}
	if label != key {
		return "", fmt.Errorf("its key label %q does not match the input key %s", label, key)
	}
	return ref, nil
}

// buildLocal reuses or builds tfsaz-live-local/<image>:in-<key> from the
// staged context and returns its image ID. Local builds are never verified.
func buildLocal(img testbedImage, key string, files []contextFile) (string, error) {
	tag := img.localTag(key)
	if id, err := output(nil, "docker", "image", "inspect", "--format", "{{.Id}}", tag); err == nil && id != "" {
		return id, nil
	}
	dir, err := os.MkdirTemp("", "tfsaz-live-context-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := stage(dir, files); err != nil {
		return "", err
	}
	step("building %s", tag)
	if err := cmd("", nil, "docker", "build", "--build-arg", imageKeyArg+"="+key, "--tag", tag, dir); err != nil {
		return "", fmt.Errorf("build %s: %w; check the Docker output and retry", tag, err)
	}
	id, err := output(nil, "docker", "image", "inspect", "--format", "{{.Id}}", tag)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("docker returned no image ID for %s", tag)
	}
	return id, nil
}

func (e *env) digestCachePath(img testbedImage, key string) string {
	return e.path("images", img.name, strings.ReplaceAll(imagePlatform, "/", "-"), key)
}

func (e *env) cachedDigest(img testbedImage, key string) (string, bool) {
	data, err := os.ReadFile(e.digestCachePath(img, key))
	digest := strings.TrimSpace(string(data))
	return digest, err == nil && digestPattern.MatchString(digest)
}

// cacheDigest records a verified digest; the rename keeps concurrent
// instances from reading a partial file.
func (e *env) cacheDigest(img testbedImage, key, digest string) error {
	if !digestPattern.MatchString(digest) {
		return errors.New("invalid digest " + digest)
	}
	path := e.digestCachePath(img, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".digest-")
	if err != nil {
		return err
	}
	_, werr := tmp.WriteString(digest + "\n")
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
