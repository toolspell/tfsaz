package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

// deadcodeAllow marks a function kept on purpose although deadcode finds it
// unreachable, in its doc comment, or a whole package in its package comment.
const deadcodeAllow = "deadcode-allow:"

// deadcodeGate fails on every unreachable function deadcode reports for each
// module and system, except those marked with deadcodeAllow.
func deadcodeGate(modules []string) step {
	return step{name: "deadcode", run: eachTarget(modules, func(dir, goos string) func(*bytes.Buffer) error {
		return func(out *bytes.Buffer) error {
			var report bytes.Buffer
			if err := toolFor(dir, goos, "deadcode", "-json", "./...")(&report); err != nil {
				out.Write(report.Bytes())
				return err
			}
			dead, err := unallowedDeadcode(dir, report.Bytes())
			if err != nil {
				return err
			}
			for _, d := range dead {
				fmt.Fprintln(out, d)
			}
			if len(dead) > 0 {
				return fmt.Errorf("%d unreachable functions; delete them, or mark one kept on purpose with %q and a reason in its doc comment", len(dead), deadcodeAllow+" <reason>")
			}
			return nil
		}
	})}
}

type deadcodePackage struct {
	Funcs []struct {
		Name     string
		Position struct {
			File string
			Line int
		}
	}
}

// unallowedDeadcode returns "file:line: name" for each function in deadcode's
// JSON report that carries no deadcodeAllow marker. Paths in the report are
// relative to dir.
func unallowedDeadcode(dir string, report []byte) ([]string, error) {
	var pkgs []deadcodePackage
	if err := json.Unmarshal(report, &pkgs); err != nil {
		return nil, fmt.Errorf("deadcode -json: %v", err)
	}
	m := markers{fset: token.NewFileSet(), files: map[string]*ast.File{}, pkgs: map[string]bool{}}
	var dead []string
	for _, pkg := range pkgs {
		for _, fn := range pkg.Funcs {
			file := filepath.Join(dir, filepath.FromSlash(fn.Position.File))
			allowed, err := m.allowed(file, fn.Position.Line)
			if err != nil {
				return nil, err
			}
			if !allowed {
				dead = append(dead, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(file), fn.Position.Line, fn.Name))
			}
		}
	}
	return dead, nil
}

// markers finds deadcodeAllow in parsed files, caching each file and package.
type markers struct {
	fset  *token.FileSet
	files map[string]*ast.File
	pkgs  map[string]bool
}

func (m markers) parse(name string) (*ast.File, error) {
	if f, ok := m.files[name]; ok {
		return f, nil
	}
	f, err := parser.ParseFile(m.fset, name, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	m.files[name] = f
	return f, nil
}

// allowed reports whether the function declared at line of file, or its
// package, carries the marker.
func (m markers) allowed(file string, line int) (bool, error) {
	f, err := m.parse(file)
	if err != nil {
		return false, err
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && m.fset.Position(fn.Name.Pos()).Line == line {
			if hasMarker(fn.Doc) {
				return true, nil
			}
			break
		}
	}
	dir := filepath.Dir(file)
	if allowed, ok := m.pkgs[dir]; ok {
		return allowed, nil
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return false, err
	}
	allowed := false
	for _, name := range names {
		pf, err := m.parse(name)
		if err != nil {
			return false, err
		}
		allowed = allowed || hasMarker(pf.Doc)
	}
	m.pkgs[dir] = allowed
	return allowed, nil
}

func hasMarker(doc *ast.CommentGroup) bool {
	return doc != nil && strings.Contains(doc.Text(), deadcodeAllow)
}
