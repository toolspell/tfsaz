package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// callsWithPrefix returns the recorded calls that start with prefix.
func callsWithPrefix(calls []string, prefix string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func assertNoCalls(t *testing.T, calls []string, prefixes ...string) {
	t.Helper()
	for _, p := range prefixes {
		if got := callsWithPrefix(calls, p); len(got) != 0 {
			t.Errorf("calls %q, want none starting with %q (all: %q)", got, p, calls)
		}
	}
}

// seedDigest caches a verified digest for the image and returns its reference.
func seedDigest(t *testing.T, e *env, img testbedImage, key string) string {
	t.Helper()
	digest := "sha256:" + strings.Repeat("c", 63) + string("0123456789"[len(img.name)])
	if err := e.cacheDigest(img, key, digest); err != nil {
		t.Fatal(err)
	}
	return img.remote() + "@" + digest
}

// wantLocalBuilds asserts that every image was built locally and that Compose
// gets the local build's image ID and never a ghcr.io reference.
func wantLocalBuilds(t *testing.T, h *fakeHost, vars []string, keys map[string]string) {
	t.Helper()
	var wantVars, wantBuilds []string
	for _, img := range testbedImages {
		tag := img.localTag(keys[img.name])
		wantBuilds = append(wantBuilds, tag)
		wantVars = append(wantVars, img.envVar()+"="+fakeID("built "+tag))
	}
	if !slices.Equal(vars, wantVars) {
		t.Errorf("Compose variables %q, want %q", vars, wantVars)
	}
	if !slices.Equal(h.docker.builds, wantBuilds) {
		t.Errorf("built %q, want %q", h.docker.builds, wantBuilds)
	}
	for _, v := range vars {
		if strings.Contains(v, "ghcr.io") {
			t.Errorf("Compose variable %q names a registry image", v)
		}
	}
}

func wantNoCache(t *testing.T, e *env, keys map[string]string) {
	t.Helper()
	for _, img := range testbedImages {
		if digest, ok := e.cachedDigest(img, keys[img.name]); ok {
			t.Errorf("cached %s for %s, want nothing cached", digest, img.name)
		}
	}
}

func TestResolveImagesCacheHitPresent(t *testing.T) {
	h := newFakeHost(t)
	h.docker.online = false
	h.gh.online = false
	e := newImageEnv(t)
	keys := testbedKeys(t)
	var wantVars []string
	for _, img := range testbedImages {
		ref := seedDigest(t, e, img, keys[img.name])
		h.docker.local[ref] = fakeImage{id: "sha256:id-" + img.name}
		wantVars = append(wantVars, img.envVar()+"="+ref)
	}

	vars, err := e.resolveImages()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(vars, wantVars) {
		t.Errorf("Compose variables %q, want %q", vars, wantVars)
	}
	assertNoCalls(t, h.calls, "gh", "docker pull", "docker build", "docker image rm")
	if len(h.docker.builds) != 0 {
		t.Errorf("built %q, want no build", h.docker.builds)
	}
	wantCalls := []string{"docker version --format {{.Server.Os}}/{{.Server.Arch}}"}
	for _, v := range wantVars {
		_, ref, _ := strings.Cut(v, "=")
		wantCalls = append(wantCalls, "docker image inspect --format {{.Os}}/{{.Architecture}} "+ref)
	}
	if !slices.Equal(h.calls, wantCalls) {
		t.Errorf("calls %q, want %q", h.calls, wantCalls)
	}
}

func TestResolveImagesCacheHitMissingPullsByDigest(t *testing.T) {
	h := newFakeHost(t)
	e := newImageEnv(t)
	keys := testbedKeys(t)
	var wantVars []string
	for _, img := range testbedImages {
		ref := seedDigest(t, e, img, keys[img.name])
		h.docker.registry[ref] = fakeImage{id: "sha256:id-" + img.name, digest: strings.TrimPrefix(ref, img.remote()+"@")}
		wantVars = append(wantVars, img.envVar()+"="+ref)
	}

	vars, err := e.resolveImages()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(vars, wantVars) {
		t.Errorf("Compose variables %q, want %q", vars, wantVars)
	}
	var wantPulls []string
	for _, v := range wantVars {
		_, ref, _ := strings.Cut(v, "=")
		wantPulls = append(wantPulls, "docker pull --platform linux/arm64 "+ref)
	}
	if got := callsWithPrefix(h.calls, "docker pull"); !slices.Equal(got, wantPulls) {
		t.Errorf("pulls %q, want %q", got, wantPulls)
	}
	assertNoCalls(t, h.calls, "gh", "docker build", "docker image rm")
	for _, c := range h.calls {
		if strings.Contains(c, ":in-") {
			t.Errorf("call %q uses a tag, want digests only", c)
		}
	}
}

func TestResolveImagesRejectedPullBuildsLocally(t *testing.T) {
	for _, tc := range []struct {
		name         string
		attested     bool
		label        func(key string) string
		platform     string
		wantLabelUse bool
	}{
		{"verification fails", false, func(key string) string { return key }, "", false},
		{"wrong key label", true, func(string) string { return strings.Repeat("0", 64) }, "", true},
		{"wrong platform", true, func(key string) string { return key }, "linux/amd64", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t)
			e := newImageEnv(t)
			keys := testbedKeys(t)
			for _, img := range testbedImages {
				ref := h.publish(img, keys[img.name], tc.label(keys[img.name]))
				h.gh.verified["oci://"+ref] = tc.attested
				for _, r := range []string{ref, img.remote() + ":" + imageTag(keys[img.name])} {
					published := h.docker.registry[r]
					published.platform = tc.platform
					h.docker.registry[r] = published
				}
			}

			vars, err := e.resolveImages()
			if err != nil {
				t.Fatal(err)
			}
			wantLocalBuilds(t, h, vars, keys)
			wantNoCache(t, e, keys)
			assertNoCalls(t, h.calls, "docker image rm")
			for _, img := range testbedImages {
				tagRef := img.remote() + ":" + imageTag(keys[img.name])
				if call := "docker pull --platform linux/arm64 " + tagRef; !slices.Contains(h.calls, call) {
					t.Errorf("missing call %q in %q", call, h.calls)
				}
				labelCall := `docker image inspect --format {{.Os}}/{{.Architecture}} {{index .Config.Labels "org.toolspell.tfsaz-live.key"}}`
				if got := len(callsWithPrefix(h.calls, labelCall)) > 0; got != tc.wantLabelUse {
					t.Errorf("label inspected = %v, want %v", got, tc.wantLabelUse)
				}
			}
			if n := len(callsWithPrefix(h.calls, "gh attestation verify")); n != len(testbedImages) {
				t.Errorf("%d attestation checks, want %d", n, len(testbedImages))
			}
		})
	}
}

func TestResolveImagesEngineNotArm64(t *testing.T) {
	for _, tc := range []struct {
		name            string
		engine          string
		defaultPlatform string
		cached          bool
	}{
		{"nothing cached", "linux/amd64", "", false},
		{"verified digest cached", "linux/amd64", "", true},
		{"DOCKER_DEFAULT_PLATFORM selects amd64", imagePlatform, "linux/amd64", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.docker.platform = tc.engine
			t.Setenv("DOCKER_DEFAULT_PLATFORM", tc.defaultPlatform)
			e := newImageEnv(t)
			keys := testbedKeys(t)
			for _, img := range testbedImages {
				ref := h.publish(img, keys[img.name], keys[img.name])
				h.gh.verified["oci://"+ref] = true
				if tc.cached {
					if err := e.cacheDigest(img, keys[img.name], strings.TrimPrefix(ref, img.remote()+"@")); err != nil {
						t.Fatal(err)
					}
					h.docker.local[ref] = h.docker.registry[ref]
				}
			}

			vars, err := e.resolveImages()
			if err != nil {
				t.Fatal(err)
			}
			wantLocalBuilds(t, h, vars, keys)
			assertNoCalls(t, h.calls, "gh", "docker pull", "docker image rm")
			for _, c := range h.calls {
				if strings.Contains(c, "ghcr.io") {
					t.Errorf("call %q touches the registry image", c)
				}
			}
		})
	}
}

func TestResolveImagesRefusesWindowsContainers(t *testing.T) {
	h := newFakeHost(t)
	h.docker.platform = "windows/amd64"
	if _, err := newImageEnv(t).resolveImages(); err == nil || !strings.Contains(err.Error(), "switch it to Linux containers") {
		t.Fatalf("err %v, want advice to switch to Linux containers", err)
	}
	assertNoCalls(t, h.calls, "gh", "docker pull", "docker build")
}

func TestResolveImagesGHNotUsable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  string
		signedIn bool
		wantAuth int
	}{
		{"gh missing", "", true, 0},
		{"gh older than 2.102.0", "2.101.9", true, 0},
		{"gh not signed in", "2.102.0", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHost(t)
			h.gh.version = tc.version
			h.gh.signedIn = tc.signedIn
			e := newImageEnv(t)
			keys := testbedKeys(t)
			for _, img := range testbedImages {
				ref := h.publish(img, keys[img.name], keys[img.name])
				h.gh.verified["oci://"+ref] = true
			}

			vars, err := e.resolveImages()
			if err != nil {
				t.Fatal(err)
			}
			wantLocalBuilds(t, h, vars, keys)
			wantNoCache(t, e, keys)
			assertNoCalls(t, h.calls, "docker pull", "gh attestation")
			if n := countCalls(h.calls, "gh --version"); n != 1 {
				t.Errorf("checked gh %d times, want once for both images", n)
			}
			if n := countCalls(h.calls, "gh auth status --active --hostname github.com"); n != tc.wantAuth {
				t.Errorf("gh auth status %d times, want %d", n, tc.wantAuth)
			}
		})
	}
}

func TestResolveImagesOffline(t *testing.T) {
	t.Run("registry unreachable without cache", func(t *testing.T) {
		h := newFakeHost(t)
		h.docker.online = false
		e := newImageEnv(t)
		keys := testbedKeys(t)

		vars, err := e.resolveImages()
		if err != nil {
			t.Fatal(err)
		}
		wantLocalBuilds(t, h, vars, keys)
		wantNoCache(t, e, keys)
		for _, img := range testbedImages {
			call := "docker pull --platform linux/arm64 " + img.remote() + ":" + imageTag(keys[img.name])
			if !slices.Contains(h.calls, call) {
				t.Errorf("missing call %q in %q", call, h.calls)
			}
		}
		assertNoCalls(t, h.calls, "gh attestation")
	})

	t.Run("existing local build is reused", func(t *testing.T) {
		h := newFakeHost(t)
		h.docker.online = false
		h.gh.online = false
		e := newImageEnv(t)
		keys := testbedKeys(t)
		var wantVars []string
		for _, img := range testbedImages {
			tag := img.localTag(keys[img.name])
			id := "sha256:existing-" + img.name
			h.docker.local[tag] = fakeImage{id: id, label: keys[img.name]}
			wantVars = append(wantVars, img.envVar()+"="+id)
		}

		vars, err := e.resolveImages()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(vars, wantVars) {
			t.Errorf("Compose variables %q, want %q", vars, wantVars)
		}
		assertNoCalls(t, h.calls, "docker build", "docker pull", "gh attestation")
		if len(h.docker.builds) != 0 {
			t.Errorf("built %q, want no build", h.docker.builds)
		}
	})
}

// TestNonStartingCommandsSkipImages covers contract item 7: only starting
// services resolves images.
func TestNonStartingCommandsSkipImages(t *testing.T) {
	h := newFakeHost(t)
	// An exported override must not reach Compose; the defaults come later
	// in the environment and win.
	t.Setenv("TFSAZ_LIVE_MOCK_IMAGE", "example.com/unverified:latest")
	var envs [][]string
	var calls []string
	fakeCmd, fakeOutput := cmd, output
	cmd = func(dir string, extraEnv []string, bin string, args ...string) error {
		if len(args) > 0 && args[0] == "compose" {
			envs = append(envs, extraEnv)
		}
		return fakeCmd(dir, extraEnv, bin, args...)
	}
	output = func(extraEnv []string, bin string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "compose" {
			envs = append(envs, extraEnv)
		}
		if bin == "docker" && len(args) > 1 && args[0] == "network" && args[1] == "ls" {
			h.calls = append(h.calls, "docker "+strings.Join(args, " "))
			return "", nil
		}
		return fakeOutput(extraEnv, bin, args...)
	}
	network := slotNetwork(0)
	e := &env{home: t.TempDir(), bed: t.TempDir(), instance: "first", network: &network}

	var status bytes.Buffer
	for name, run := range map[string]func() error{
		"services status": func() error { return e.services("status") },
		"services down":   func() error { return e.services("down") },
		"composeDown":     e.composeDown,
		"compose":         func() error { return e.compose("ps") },
		"status json":     func() error { return e.servicesStatusJSON(&status) },
		"serviceRunning":  func() error { _, err := e.serviceRunning("router"); return err },
	} {
		if err := run(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	calls = h.calls
	if len(calls) == 0 {
		t.Fatal("no commands ran")
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "docker compose ") && !strings.HasPrefix(c, "docker network ls ") {
			t.Errorf("unexpected call %q", c)
		}
		if strings.Contains(c, "--rmi") {
			t.Errorf("call %q removes images", c)
		}
	}
	assertNoCalls(t, calls, "gh", "docker version", "docker image", "docker pull", "docker build")
	wantImages := []string{"TFSAZ_LIVE_MOCK_IMAGE=tfsaz-live-local/mock:unresolved", "TFSAZ_LIVE_ROUTER_IMAGE=tfsaz-live-local/router:unresolved"}
	if len(envs) == 0 {
		t.Fatal("no Compose command ran")
	}
	for _, extra := range envs {
		var images []string
		for _, v := range extra {
			if strings.HasPrefix(v, "TFSAZ_LIVE_MOCK_IMAGE=") || strings.HasPrefix(v, "TFSAZ_LIVE_ROUTER_IMAGE=") {
				images = append(images, v)
			}
		}
		if !slices.Equal(images, wantImages) {
			t.Errorf("Compose got image variables %q, want the defaults %q", images, wantImages)
		}
	}
	if _, err := os.Stat(e.path("images")); err == nil {
		t.Error("an images cache directory was created")
	}
}

// TestComposeConfigImages runs the real `docker compose config`, without
// starting containers, on the generated environment.
func TestComposeConfigImages(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose unavailable")
	}
	state := t.TempDir()
	for _, dir := range []string{"mock-keys", "router"} {
		if err := os.MkdirAll(filepath.Join(state, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "Corefile"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bed, err := filepath.Abs(repoBed)
	if err != nil {
		t.Fatal(err)
	}
	network := slotNetwork(0)
	base := network.composeEnv("tfsaz-live-configtest", state)
	const (
		mockRef   = "ghcr.io/toolspell/tfsaz-live-mock@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		routerRef = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	for _, tc := range []struct {
		name                 string
		images               []string
		wantMock, wantRouter string
	}{
		{"exact references", []string{"TFSAZ_LIVE_MOCK_IMAGE=" + mockRef, "TFSAZ_LIVE_ROUTER_IMAGE=" + routerRef}, mockRef, routerRef},
		{"defaults", nil, "tfsaz-live-local/mock:unresolved", "tfsaz-live-local/router:unresolved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := exec.Command("docker", "compose", "-f", filepath.Join(bed, "compose.yaml"), "config", "--format", "json")
			c.Dir = bed
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "TFSAZ_LIVE_") {
					c.Env = append(c.Env, v)
				}
			}
			c.Env = append(c.Env, base...)
			c.Env = append(c.Env, tc.images...)
			var stderr bytes.Buffer
			c.Stderr = &stderr
			out, err := c.Output()
			if err != nil {
				t.Fatalf("docker compose config: %v\n%s", err, stderr.String())
			}
			var cfg struct {
				Services map[string]struct {
					Image      string `json:"image"`
					PullPolicy string `json:"pull_policy"`
				} `json:"services"`
			}
			if err := json.Unmarshal(out, &cfg); err != nil {
				t.Fatal(err)
			}
			for service, want := range map[string]string{"devops-server-mock": tc.wantMock, "router": tc.wantRouter} {
				got := cfg.Services[service]
				if got.Image != want {
					t.Errorf("%s image %q, want %q", service, got.Image, want)
				}
				if got.PullPolicy != "never" {
					t.Errorf("%s pull_policy %q, want never", service, got.PullPolicy)
				}
			}
		})
	}
}
