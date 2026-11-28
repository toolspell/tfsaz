package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeHost replaces docker and gh behind the cmd and output seams. Each
// command the tool runs is recorded as "bin arg...", and unknown commands
// fail the test, so a test sees every docker and gh call.
type fakeHost struct {
	t      *testing.T
	calls  []string
	docker *fakeDocker
	gh     *fakeGH
	// composeEnv is the extra environment of the last Compose command.
	composeEnv []string
}

// fakeImage is an image in the fake registry or the fake local store.
type fakeImage struct {
	id       string
	digest   string // sha256:..., the registry digest
	label    string // value of imageKeyLabel
	platform string // OS/arch; empty means imagePlatform
}

func (img fakeImage) osArch() string { return cmp.Or(img.platform, imagePlatform) }

type fakeDocker struct {
	platform string               // engine OS/arch
	online   bool                 // registry reachable
	registry map[string]fakeImage // registry references: REPO:TAG and REPO@DIGEST
	local    map[string]fakeImage // local references: tags, REPO@DIGEST and IDs
	builds   []string             // tags built, in order
}

type fakeGH struct {
	version  string // "" when gh is missing
	signedIn bool
	online   bool
	verified map[string]bool // oci:// references whose attestations verify
}

// newFakeHost installs a host with an arm64 engine, an empty local store, an
// empty registry and a signed-in gh 2.102.0, all online.
func newFakeHost(t *testing.T) *fakeHost {
	h := &fakeHost{
		t:      t,
		docker: &fakeDocker{platform: imagePlatform, online: true, registry: map[string]fakeImage{}, local: map[string]fakeImage{}},
		gh:     &fakeGH{version: minGHVersion, signedIn: true, online: true, verified: map[string]bool{}},
	}
	t.Setenv("DOCKER_DEFAULT_PLATFORM", "")
	oldCmd, oldOutput := cmd, output
	t.Cleanup(func() { cmd, output = oldCmd, oldOutput })
	cmd = func(_ string, extraEnv []string, bin string, args ...string) error {
		_, err := h.run(extraEnv, bin, args)
		return err
	}
	output = func(extraEnv []string, bin string, args ...string) (string, error) {
		return h.run(extraEnv, bin, args)
	}
	return h
}

func (h *fakeHost) run(extraEnv []string, bin string, args []string) (string, error) {
	h.calls = append(h.calls, strings.Join(append([]string{bin}, args...), " "))
	switch bin {
	case "docker":
		if len(args) > 0 && args[0] == "compose" {
			h.composeEnv = slices.Clone(extraEnv)
			return "", nil
		}
		return h.docker.run(h.t, args)
	case "gh":
		return h.gh.run(h.t, args)
	}
	h.t.Fatalf("unexpected command: %s %q", bin, args)
	return "", nil
}

func (d *fakeDocker) run(t *testing.T, args []string) (string, error) {
	command := strings.Join(args, " ")
	switch {
	case command == "version --format {{.Server.Os}}/{{.Server.Arch}}":
		return d.platform, nil
	case len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[2] == "--format":
		img, ok := d.local[args[4]]
		if !ok {
			return "", fmt.Errorf("no such image: %s", args[4])
		}
		switch args[3] {
		case "{{.Id}}":
			return img.id, nil
		case "{{.Os}}/{{.Architecture}}":
			return img.osArch(), nil
		case `{{.Os}}/{{.Architecture}} {{index .Config.Labels "` + imageKeyLabel + `"}}`:
			return strings.TrimSpace(img.osArch() + " " + img.label), nil
		}
	case len(args) == 4 && args[0] == "pull" && args[1] == "--platform" && args[2] == imagePlatform:
		if !d.online {
			return "", errors.New("dial tcp: lookup ghcr.io: no such host")
		}
		img, ok := d.registry[args[3]]
		if !ok {
			return "", fmt.Errorf("%s: not found", args[3])
		}
		repo, _, _ := strings.Cut(args[3], "@")
		repo, _, _ = strings.Cut(repo, ":in-")
		d.local[args[3]] = img
		d.local[repo+"@"+img.digest] = img
		d.local[img.id] = img
		return "Digest: " + img.digest + "\nStatus: Downloaded newer image for " + args[3] + "\n" + args[3], nil
	case len(args) == 3 && args[0] == "image" && args[1] == "rm":
		if _, ok := d.local[args[2]]; !ok {
			return "", fmt.Errorf("no such image: %s", args[2])
		}
		delete(d.local, args[2])
		return "", nil
	case len(args) == 9 && args[0] == "build" && args[1] == "--load" && args[2] == "--build-arg" && args[4] == "--iidfile" && args[6] == "--tag":
		key, ok := strings.CutPrefix(args[3], imageKeyArg+"=")
		if !ok {
			break
		}
		if _, err := os.Stat(filepath.Join(args[8], "Dockerfile")); err != nil {
			t.Errorf("build context %s has no Dockerfile: %v", args[8], err)
		}
		tag := args[7]
		img := fakeImage{id: fakeID("built " + tag), label: key}
		if err := os.WriteFile(args[5], []byte(img.id), 0o600); err != nil {
			t.Fatal(err)
		}
		d.local[tag] = img
		d.local[img.id] = img
		d.builds = append(d.builds, tag)
		return "", nil
	}
	t.Fatalf("unexpected docker command: %q", args)
	return "", nil
}

func (g *fakeGH) run(t *testing.T, args []string) (string, error) {
	command := strings.Join(args, " ")
	switch {
	case g.version == "":
		return "", errors.New(`exec: "gh": executable file not found in $PATH`)
	case command == "--version":
		return "gh version " + g.version + " (2026-11-01)\nhttps://github.com/cli/cli/releases/tag/v" + g.version, nil
	case command == "auth status --active --hostname github.com":
		if !g.signedIn || !g.online {
			return "", errors.New("exit status 1")
		}
		return "", nil
	case len(args) == 11 && args[0] == "attestation" && args[1] == "verify":
		want := []string{"--hostname", "github.com", "--repo", "toolspell/tfsaz",
			"--signer-workflow", "github.com/toolspell/tfsaz/.github/workflows/testbed-images.yml", "--source-ref", "refs/heads/main"}
		if !slices.Equal(args[3:], want) {
			t.Errorf("attestation verify flags %q, want %q", args[3:], want)
		}
		if !g.online || !g.verified[args[2]] {
			return "", errors.New("exit status 1")
		}
		return "", nil
	}
	t.Fatalf("unexpected gh command: %q", args)
	return "", nil
}

// testbedKeys returns each image's key from the repository's live-tests.
func testbedKeys(t *testing.T) map[string]string {
	keys := map[string]string{}
	for _, img := range testbedImages {
		files, err := img.readContext(repoBed)
		if err != nil {
			t.Fatal(err)
		}
		keys[img.name] = img.key(files)
	}
	return keys
}

// publish puts an image for key into the fake registry under its key tag
// and returns its digest reference.
func (h *fakeHost) publish(img testbedImage, key, label string) string {
	digest := "sha256:" + strings.Repeat(fmt.Sprintf("%x", len(img.name)), 64)[:64]
	published := fakeImage{id: fakeID("published " + img.name), digest: digest, label: label}
	ref := img.remote() + "@" + digest
	h.docker.registry[img.remote()+":"+imageTag(key)] = published
	h.docker.registry[ref] = published
	return ref
}

// fakeID returns a valid image ID derived from s.
func fakeID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newImageEnv(t *testing.T) *env {
	return &env{home: t.TempDir(), bed: repoBed, instance: "first"}
}

func TestResolveImagesVerifiedPull(t *testing.T) {
	h := newFakeHost(t)
	e := newImageEnv(t)
	keys := testbedKeys(t)
	want := map[string]string{}
	for _, img := range testbedImages {
		ref := h.publish(img, keys[img.name], keys[img.name])
		h.gh.verified["oci://"+ref] = true
		want[img.name] = ref
	}

	vars, err := e.resolveImages()
	if err != nil {
		t.Fatal(err)
	}
	wantVars := []string{"TFSAZ_LIVE_MOCK_IMAGE=" + want["mock"], "TFSAZ_LIVE_ROUTER_IMAGE=" + want["router"]}
	if !slices.Equal(vars, wantVars) {
		t.Errorf("Compose variables %q, want %q", vars, wantVars)
	}
	if len(h.docker.builds) != 0 {
		t.Errorf("built %q, want verified pulls only", h.docker.builds)
	}
	for _, img := range testbedImages {
		ref := want[img.name]
		for _, call := range []string{
			"docker pull --platform linux/arm64 " + img.remote() + ":" + imageTag(keys[img.name]),
			"gh attestation verify oci://" + ref + " --hostname github.com --repo toolspell/tfsaz --signer-workflow github.com/toolspell/tfsaz/.github/workflows/testbed-images.yml --source-ref refs/heads/main",
			`docker image inspect --format {{.Os}}/{{.Architecture}} {{index .Config.Labels "org.toolspell.tfsaz-live.key"}} ` + ref,
		} {
			if !slices.Contains(h.calls, call) {
				t.Errorf("missing call %q in %q", call, h.calls)
			}
		}
		if digest, ok := e.cachedDigest(img, keys[img.name]); !ok || img.remote()+"@"+digest != ref {
			t.Errorf("cached digest for %s = %q, %v; want %s", img.name, digest, ok, ref)
		}
	}
	if n := countCalls(h.calls, "gh --version"); n != 1 {
		t.Errorf("checked gh %d times, want once", n)
	}
}

func countCalls(calls []string, call string) int {
	n := 0
	for _, c := range calls {
		if c == call {
			n++
		}
	}
	return n
}
