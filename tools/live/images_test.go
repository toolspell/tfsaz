package main

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

var repoBed = filepath.Join("..", "..", "live-tests")

func TestImageKey(t *testing.T) {
	img := testbedImage{name: "test"}
	files := []contextFile{{"Dockerfile", []byte("FROM scratch\n")}, {"a.sh", []byte("echo a\n")}}
	key := img.key(files)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(key) {
		t.Fatalf("key %q is not 64 hex digits", key)
	}
	if again := img.key(files); again != key {
		t.Errorf("key changed between runs: %s, then %s", key, again)
	}
	if reversed := img.key([]contextFile{files[1], files[0]}); reversed != key {
		t.Errorf("key depends on file order: %s, reversed %s", key, reversed)
	}

	differs := map[string]struct {
		img   testbedImage
		files []contextFile
	}{
		"content":         {img, []contextFile{files[0], {"a.sh", []byte("echo b\n")}}},
		"path":            {img, []contextFile{files[0], {"b.sh", files[1].data}}},
		"added file":      {img, append(slices.Clone(files), contextFile{"c", nil})},
		"removed file":    {img, files[:1]},
		"target":          {testbedImage{target: "build"}, files},
		"build argument":  {testbedImage{buildArgs: []string{"A=1"}}, files},
		"split build arg": {testbedImage{buildArgs: []string{"A=", "1"}}, files},
	}
	for name, tc := range differs {
		if got := tc.img.key(tc.files); got == key {
			t.Errorf("%s: key unchanged", name)
		}
	}
}

func TestImageKeyFraming(t *testing.T) {
	var img testbedImage
	for name, pair := range map[string][2][]contextFile{
		"path and content boundary": {
			{{"ab", []byte("c")}},
			{{"a", []byte("bc")}},
		},
		"file boundary": {
			{{"a", []byte("xb")}, {"c", nil}},
			{{"a", []byte("x")}, {"bc", nil}},
		},
		"empty file or none": {
			{{"a", []byte("x")}},
			{{"a", []byte("x")}, {"b", nil}},
		},
		"forged length prefix": {
			{{"a", append([]byte("x"), []byte("\x00\x00\x00\x00\x00\x00\x00\x01b\x00\x00\x00\x00\x00\x00\x00\x00")...)}},
			{{"a", []byte("x")}, {"b", nil}},
		},
	} {
		if a, b := img.key(pair[0]), img.key(pair[1]); a == b {
			t.Errorf("%s: both contexts hash to %s", name, a)
		}
	}
}

func TestImageContexts(t *testing.T) {
	want := map[string][]string{
		"router": {"Dockerfile", "dnsmasq.conf", "entrypoint.sh", "peer.sh"},
	}
	for _, img := range testbedImages {
		t.Run(img.name, func(t *testing.T) {
			files, err := img.readContext(repoBed)
			if err != nil {
				t.Fatal(err)
			}
			var paths []string
			for _, f := range files {
				paths = append(paths, f.path)
			}
			if w, ok := want[img.name]; ok && !slices.Equal(paths, w) {
				t.Errorf("context %q, want %q", paths, w)
			}
			for _, required := range img.files {
				if !slices.Contains(paths, required) {
					t.Errorf("context %q lacks %s", paths, required)
				}
			}
			for _, p := range paths {
				if strings.HasSuffix(p, "_test.go") {
					t.Errorf("context has test file %s", p)
				}
			}
			if img.goSources && !slices.Contains(paths, "main.go") {
				t.Errorf("context %q lacks the Go sources", paths)
			}

			dir := filepath.Join(t.TempDir(), "context")
			if err := stage(dir, files); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != len(files) {
				t.Fatalf("staged %d entries, err %v; want %d files", len(entries), err, len(files))
			}
			for _, f := range files {
				path := filepath.Join(dir, f.path)
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(data, f.data) {
					t.Errorf("staged %s differs, err %v", f.path, err)
				}
				if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
					t.Errorf("staged %s: mode %v, err %v; want 0644", f.path, info.Mode(), err)
				}
			}
			if err := stage(dir, files); err == nil || !strings.Contains(err.Error(), "not empty") {
				t.Errorf("staging into a non-empty directory: err %v", err)
			}
		})
	}
}

func TestImageContextRejectsSymlinks(t *testing.T) {
	bed := t.TempDir()
	img := testbedImage{name: "test", dir: "img", files: []string{"Dockerfile", "peer.sh"}}
	dir := filepath.Join(bed, "img")
	outside := filepath.Join(bed, "secret")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{filepath.Join(dir, "Dockerfile"): "FROM scratch\n", outside: "secret"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "peer.sh")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := img.readContext(bed); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("symlinked file: err %v, want a refusal", err)
	}

	linked := testbedImage{name: "linked", dir: "linked", files: []string{"Dockerfile"}}
	if err := os.Symlink(dir, filepath.Join(bed, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := linked.readContext(bed); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("symlinked image directory: err %v, want a refusal", err)
	}

	target := filepath.Join(bed, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(bed, "out")); err != nil {
		t.Fatal(err)
	}
	files := []contextFile{{"Dockerfile", []byte("FROM scratch\n")}}
	if err := stage(filepath.Join(bed, "out"), files); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("staging into a symlink: err %v, want a refusal", err)
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Errorf("staging wrote through the symlink: %v", entries)
	}
}

// TestDockerfilesUseOnlyTheContext keeps each Dockerfile buildable from its
// staged context: it copies only listed files (dropped test files aside),
// pins every base image by digest and labels the image with its key.
func TestDockerfilesUseOnlyTheContext(t *testing.T) {
	keyLabel := "LABEL " + imageKeyLabel + "=$" + imageKeyArg
	for _, img := range testbedImages {
		t.Run(img.name, func(t *testing.T) {
			files, err := img.readContext(repoBed)
			if err != nil {
				t.Fatal(err)
			}
			context := map[string]bool{}
			for _, f := range files {
				context[f.path] = true
			}
			dir := filepath.Join(repoBed, img.dir)
			data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
			if err != nil {
				t.Fatal(err)
			}
			stages := map[string]bool{}
			for _, line := range dockerInstructions(string(data)) {
				fields := strings.Fields(line)
				switch strings.ToUpper(fields[0]) {
				case "FROM":
					if !stages[fields[1]] && !regexp.MustCompile(`^[^@\s]+:[^@\s]+@sha256:[0-9a-f]{64}$`).MatchString(fields[1]) {
						t.Errorf("base image %s is not pinned as NAME:TAG@sha256:DIGEST", fields[1])
					}
					if len(fields) == 4 && strings.EqualFold(fields[2], "AS") {
						stages[fields[3]] = true
					}
				case "ADD":
					t.Errorf("ADD is not allowed, use COPY: %s", line)
				case "COPY":
					if i := slices.IndexFunc(fields, func(s string) bool { return strings.HasPrefix(s, "--from=") }); i >= 0 {
						if from := strings.TrimPrefix(fields[i], "--from="); !stages[from] {
							t.Errorf("COPY --from=%s is not an earlier stage", from)
						}
						continue
					}
					args := slices.DeleteFunc(fields[1:], func(s string) bool { return strings.HasPrefix(s, "--") })
					for _, pattern := range args[:len(args)-1] {
						matches, err := filepath.Glob(filepath.Join(dir, pattern))
						if err != nil {
							t.Fatal(err)
						}
						inContext := 0
						for _, m := range matches {
							rel, err := filepath.Rel(dir, m)
							if err != nil {
								t.Fatal(err)
							}
							rel = filepath.ToSlash(rel)
							switch {
							case context[rel]:
								inContext++
							case path.Dir(rel) != "." || !strings.HasSuffix(rel, "_test.go"):
								t.Errorf("COPY %s takes %s, which is not in the context", pattern, rel)
							}
						}
						if inContext == 0 {
							t.Errorf("COPY %s matches nothing in the context", pattern)
						}
					}
				}
			}
			if !strings.Contains(string(data), keyLabel) {
				t.Errorf("Dockerfile lacks %q", keyLabel)
			}
		})
	}
}

// dockerInstructions joins continuation lines and drops comments.
func dockerInstructions(dockerfile string) []string {
	var out []string
	var current strings.Builder
	for _, line := range strings.Split(dockerfile, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if cont, ok := strings.CutSuffix(line, "\\"); ok {
			current.WriteString(cont + " ")
			continue
		}
		current.WriteString(line)
		out = append(out, current.String())
		current.Reset()
	}
	return out
}
