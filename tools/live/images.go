package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	imagePlatform = "linux/arm64"
	// imageKeyArg passes the key into the image's imageKeyLabel; it is
	// excluded from the key it carries.
	imageKeyArg   = "TFSAZ_LIVE_INPUTS"
	imageKeyLabel = "org.toolspell.tfsaz-live.key"
	imageKeyV1    = "tfsaz-live image key v1"
)

// testbedImage is one image of the test bed, built from an explicit file
// list so that local builds and CI never depend on Docker's ignore rules.
type testbedImage struct {
	name, service, dir string
	files              []string
	// goSources adds the directory's non-test .go files.
	goSources bool
	// target and buildArgs (NAME=VALUE, never imageKeyArg) are build
	// parameters; both are empty today but part of the key.
	target    string
	buildArgs []string
}

var testbedImages = []testbedImage{
	{name: "mock", service: "devops-server-mock", dir: "devops-server-mock",
		files: []string{"Dockerfile", "go.mod", "go.sum", "catalog.json"}, goSources: true},
	{name: "router", service: "router", dir: "router",
		files: []string{"Dockerfile", "entrypoint.sh", "peer.sh", "dnsmasq.conf"}},
}

func lookupImage(name string) (testbedImage, bool) {
	for _, img := range testbedImages {
		if img.name == name {
			return img, true
		}
	}
	return testbedImage{}, false
}

func unknownImage(name string) error {
	return fmt.Errorf("unknown image %q; use mock or router", name)
}

type contextFile struct {
	path string
	data []byte
}

// readContext reads the image's build context from live-tests/<dir>,
// sorted by path. Only a real directory and regular files count, so a
// symlink cannot pull a file from outside the directory into the image.
func (img testbedImage) readContext(bed string) ([]contextFile, error) {
	dir := filepath.Join(bed, img.dir)
	if info, err := os.Lstat(dir); err != nil {
		return nil, fmt.Errorf("image %s: %w", img.name, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("image %s: %s is not a directory", img.name, dir)
	}
	names := slices.Clone(img.files)
	if img.goSources {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	files := make([]contextFile, 0, len(names))
	for _, name := range slices.Compact(names) {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("image %s: %w", img.name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("image %s: %s is not a regular file", img.name, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, contextFile{path: name, data: data})
	}
	return files, nil
}

// key hashes the build parameters and the context. Every field is
// length-prefixed, so no boundary between a path and its content, or
// between two files, can be moved without changing the key.
func (img testbedImage) key(files []contextFile) string {
	h := sha256.New()
	writeField(h, imageKeyV1)
	writeField(h, "Dockerfile")
	writeField(h, img.target)
	writeField(h, imagePlatform)
	writeCount(h, len(img.buildArgs))
	for _, arg := range img.buildArgs {
		writeField(h, arg)
	}
	sorted := slices.SortedFunc(slices.Values(files), func(a, b contextFile) int { return cmp.Compare(a.path, b.path) })
	writeCount(h, len(sorted))
	for _, f := range sorted {
		writeField(h, f.path)
		writeField(h, string(f.data))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeCount(h hash.Hash, n int) {
	_ = binary.Write(h, binary.BigEndian, uint64(n))
}

func writeField(h hash.Hash, s string) {
	writeCount(h, len(s))
	_, _ = io.WriteString(h, s)
}

func imageTag(key string) string { return "in-" + key }

// stage writes the context into dir, which must be a missing or empty
// directory, not a symlink. Each file is created new inside dir with mode
// 644, so neither a planted file nor the checkout's modes or the umask
// reach the image.
func stage(dir string, files []contextFile) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("%s is not a directory; pass a new or empty directory", dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty; pass a new or empty directory", dir)
	}
	for _, f := range files {
		if filepath.Base(f.path) != f.path {
			return fmt.Errorf("context path %q is not a plain file name", f.path)
		}
		if err := writeNew(root, f); err != nil {
			return err
		}
	}
	return nil
}

func writeNew(root *os.Root, f contextFile) error {
	file, err := root.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := file.Write(f.data)
	return errors.Join(werr, file.Chmod(0o644), file.Close())
}

func (e *env) image(action string, args []string, stdout io.Writer) error {
	img, ok := lookupImage(args[0])
	if !ok {
		return unknownImage(args[0])
	}
	files, err := img.readContext(e.bed)
	if err != nil {
		return err
	}
	key := img.key(files)
	switch action {
	case "key":
	case "context":
		dir, err := filepath.Abs(args[1])
		if err != nil {
			return err
		}
		if err := stage(dir, files); err != nil {
			return err
		}
	default:
		return errors.New("unknown image action " + action)
	}
	_, err = fmt.Fprintln(stdout, key)
	return err
}
