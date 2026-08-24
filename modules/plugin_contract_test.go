package modules

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginEntrySignatures is the CI smoke test for the Go plugin contract.
//
// The loader (module.go loadPlugin) opens each built .so and asserts the
// EXACT symbol signature sym.(func() Module). Go plugin symbol lookup does
// NOT use interface satisfaction: a func New() *MyModule type-checks in
// normal Go but fails the load-time assertion ("New() has wrong signature").
// That failure class previously only surfaced as [p]load on a live bot —
// this test catches it in CI instead.
//
// Why AST and not plugin.Open: a plugin .so can only be opened by a host
// binary linking byte-identical builds of every shared dependency package;
// a `go test` binary never matches a freshly built .so ("plugin was built
// with a different version of package …"). Parsing the sources checks the
// same contract deterministically, on every machine, with no build step.
//
// Discovery walks modules/ recursively for main.go (same convention as the
// updater's rebuildPlugins since PR #16), so new modules are covered
// automatically without touching ci.yml. Per package it verifies:
//  1. exactly one non-test `func New()` exists,
//  2. its sole result type is the interface modules.Module — resolved
//     through the file's actual import (alias-proof),
//  3. Name() returns the package directory name when written as a plain
//     string literal (the manager keys loaded modules by that name).
func TestPluginEntrySignatures(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	pkgs := discoverPluginPackages(t, filepath.Join(root, "modules"))
	if len(pkgs) == 0 {
		t.Fatal("no plugin packages discovered under modules/ — discovery is broken")
	}

	for pkgDir, pkgImport := range pkgs {
		pkgDir, pkgImport := pkgDir, pkgImport
		t.Run(pkgImport, func(t *testing.T) {
			fset := token.NewFileSet()
			files := parsePluginSources(t, fset, pkgDir)

			newDecl := findFuncDecl(files, "New")
			if newDecl == nil {
				t.Fatal("no non-test func New() declared — the loader looks up the exact symbol \"New\"")
			}
			if n := countFuncDecls(files, "New"); n != 1 {
				t.Fatalf("expected exactly one non-test func New(), found %d", n)
			}
			if newDecl.Type.Results == nil || len(newDecl.Type.Results.List) != 1 || len(newDecl.Type.Results.List[0].Names) > 1 {
				t.Fatalf("New() must return exactly one value (the loader asserts func() Module); got %s",
					debugResults(fset, newDecl))
			}
			if !isModulesModuleResult(files, newDecl.Type.Results.List[0].Type) {
				t.Fatalf("New() must return exactly modules.Module — plugin symbol lookup asserts the "+
					"exact function type and does NOT honor interface satisfaction (got %s)",
					debugResults(fset, newDecl))
			}

			nameLit := stringLiteralResult(files, "Name")
			if nameLit != "" && nameLit != filepath.Base(pkgDir) {
				t.Errorf("Name() = %q, want %q (directory name) — the module manager keys loaded modules by Name()", nameLit, filepath.Base(pkgDir))
			}
		})
	}
}

// parsePluginSources parses every non-test .go file in dir.
func parsePluginSources(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		out = append(out, f)
	}
	return out
}

// findFuncDecl returns the first non-test declaration of func name.
func findFuncDecl(files []*ast.File, name string) *ast.FuncDecl {
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
				return fn
			}
		}
	}
	return nil
}

func countFuncDecls(files []*ast.File, name string) int {
	n := 0
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
				n++
			}
		}
	}
	return n
}

// importPathFor resolves the import path an identifier refers to in a given
// file: for ident "modules" with `import "github.com/misfit/bot/modules"` it
// returns that path, honoring aliases (both named and dot-less defaults).
func importPathFor(file *ast.File, ident *ast.Ident) string {
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		alias := ""
		if imp.Name != nil {
			alias = imp.Name.Name
		} else {
			alias = path[strings.LastIndex(path, "/")+1:]
		}
		if alias == ident.Name {
			return path
		}
	}
	return ""
}

// isModulesModuleResult reports whether expr is the selector
// <alias-to-core-modules>.Module, resolving the alias through the file's own
// imports so `import core "github.com/misfit/bot/modules"` counts too.
func isModulesModuleResult(files []*ast.File, expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || sel.Sel.Name != "Module" {
		return false
	}
	const coreModulesPath = "github.com/misfit/bot/modules"
	for _, f := range files {
		if importPathFor(f, x) == coreModulesPath {
			return true
		}
	}
	return false
}

// stringLiteralResult extracts the returned string literal from a zero-arg
// method `func (r *T) Name() string { return "x" }`. Returns "" when absent
// or not a plain literal (caller then skips the comparison).
func stringLiteralResult(files []*ast.File, methodName string) string {
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != methodName {
				continue
			}
			if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if _, isStr := fn.Type.Results.List[0].Type.(*ast.Ident); !isStr || fmt.Sprintf("%s", fn.Type.Results.List[0].Type) != "string" {
				continue
			}
			if fn.Body == nil {
				continue
			}
			for _, st := range fn.Body.List {
				ret, ok := st.(*ast.ReturnStmt)
				if !ok || len(ret.Results) != 1 {
					continue
				}
				if lit, ok := ret.Results[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					return strings.Trim(lit.Value, `"`)
				}
			}
		}
	}
	return ""
}

func debugResults(fset *token.FileSet, fn *ast.FuncDecl) string {
	pos := fset.Position(fn.Pos())
	var parts []string
	if fn.Type.Results != nil {
		for _, fld := range fn.Type.Results.List {
			parts = append(parts, fmt.Sprintf("%s", fld.Type))
		}
	}
	return fmt.Sprintf("%d sig: (%s)", pos.Line, strings.Join(parts, ", "))
}

// discoverPluginPackages walks modulesDir recursively and returns one entry
// per directory containing main.go, mapping absolute dir → build import path
// relative to the repo root (e.g. modules/Go/tickets → ./modules/Go/tickets).
// Hidden directories (.git, .venv) are skipped, mirroring the updater.
func discoverPluginPackages(t *testing.T, modulesDir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(modulesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, keep walking siblings
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != modulesDir {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "main.go" {
			return nil
		}
		dir := filepath.Dir(path)
		rel, err := filepath.Rel(filepath.Dir(modulesDir), dir)
		if err != nil {
			return nil
		}
		out[dir] = "./" + filepath.ToSlash(rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", modulesDir, err)
	}
	return out
}
