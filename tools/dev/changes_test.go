package main

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestPlanFor(t *testing.T) {
	pkgDirs := []string{".", "internal/az", "tools/dev"}
	deleted := "internal/az/removed.go"
	exists := func(f string) bool { return f != deleted }
	for _, tc := range []struct {
		name      string
		files     []string
		goFiles   []string
		goChanged bool
		modules   []string
		packages  []string
		workflows bool
	}{
		{"docs only", []string{"README.md", "docs/user/install.md"}, nil, false, nil, []string{"."}, false},
		{"internal/az code", []string{"internal/az/cli.go"}, []string{"internal/az/cli.go"}, true, []string{"."}, []string{".", "./internal/az"}, false},
		{"deleted file is not formatted", []string{deleted}, nil, true, []string{"."}, []string{".", "./internal/az"}, false},
		{"testdata tests its package", []string{"internal/az/testdata/repository.json"}, nil, false, nil, []string{".", "./internal/az"}, false},
		{"root module files test everything", []string{"go.sum"}, nil, true, []string{"."}, []string{"./..."}, false},
		{"tool pin", []string{"tools/gitleaks/go.mod"}, nil, false, []string{"tools/gitleaks"}, []string{"."}, false},
		{"dev tool code", []string{"tools/dev/run.go"}, []string{"tools/dev/run.go"}, true, []string{"."}, []string{".", "./tools/dev"}, false},
		{"lint config", []string{".golangci.yml"}, nil, true, nil, []string{"."}, false},
		{"workflow", []string{".github/workflows/test.yml"}, nil, false, nil, []string{"."}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := planFor(tc.files, exists, pkgDirs)
			if !slices.Equal(p.goFiles, tc.goFiles) || p.goChanged != tc.goChanged || !slices.Equal(p.modules, tc.modules) || !slices.Equal(p.packages, tc.packages) || p.workflows != tc.workflows {
				t.Errorf("planFor(%q) = %+v", tc.files, p)
			}
		})
	}
}

func TestGofmtCheck(t *testing.T) {
	files := map[string]string{
		"ok.go":     "package a\n\nfunc f() {}\n",
		"crlf.go":   "package a\r\n\r\nfunc f() {}\r\n",
		"spaces.go": "package a\nfunc  f() {}\n",
		"broken.go": "package a\nfunc {\n",
	}
	read := func(f string) ([]byte, error) {
		src, ok := files[f]
		if !ok {
			return nil, errors.New("missing")
		}
		return []byte(src), nil
	}
	for _, tc := range []struct {
		file string
		ok   bool
	}{
		{"ok.go", true},
		{"crlf.go", true},
		{"spaces.go", false},
		{"broken.go", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			var out bytes.Buffer
			if err := gofmtCheck([]string{tc.file}, read)(&out); (err == nil) != tc.ok {
				t.Errorf("gofmtCheck(%s) error = %v, want ok %v; output %q", tc.file, err, tc.ok, out.String())
			}
		})
	}
}
