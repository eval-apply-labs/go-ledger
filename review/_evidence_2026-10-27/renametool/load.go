package main

// Loading: find the tree's modules, select and parse their files the way `go build` does, and
// type-check every package in the three variants `go test` builds.

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// moduleBuildContext decides which .go files belong to the build, as `go build` does on this
// machine (GOOS, GOARCH, release tags, tool tags, cgo setting). It is a copy of build.Default taken
// at start-up, before newStdImporter switches cgo off for the standard-library importer.
var moduleBuildContext = build.Default

// newStdImporter returns the importer for standard-library packages: go/importer's "source"
// importer, which type-checks GOROOT's sources with this tool's go/types. One importer is shared
// by every tree loaded in a run, so a standard-library object is the same pointer in every tree.
// Cgo is switched off for it, so that net and os/user are read from their pure-Go files instead of
// running `go tool cgo`; their exported API is the same either way.
func newStdImporter(fset *token.FileSet) types.ImporterFrom {
	build.Default.CgoEnabled = false
	return importer.ForCompiler(fset, "source", nil).(types.ImporterFrom)
}

// Tree is one loaded copy of the repository: every module, package, file and type-checked variant.
type Tree struct {
	Label   string             // "old", "new" or "tree", for messages
	Root    string             // absolute path
	Fset    *token.FileSet     // shared by all trees of a run
	std     types.ImporterFrom // shared standard-library importer
	overlay map[string][]byte  // relative path -> contents replacing the file on disk (apply)
	ctx     build.Context

	Modules  []*Module
	Packages []*Package          // sorted by import path
	byPath   map[string]*Package // import path -> package
	Files    []*File             // every parsed file, sorted by relative path
	ByRel    map[string]*File
	byToken  map[*token.File]*File
	Variants []*Variant // every type-checked variant, dependencies first
	Excluded []string   // .go files of module directories that the build context leaves out
	Errors   []string   // type-checking errors

	defIndex map[*Variant]map[string]types.Object // lazily built by defsByKey
	fields   map[string]*fieldDecl                // lazily built by fieldDecls
}

// Module is one go.mod found in the tree.
type Module struct {
	Path      string            // module path
	Dir       string            // directory relative to the tree root ("" for the root)
	GoVersion string            // the go line, e.g. "1.27"
	Toolchain string            // the toolchain line, e.g. "go1.27.3"
	Replace   map[string]string // replaced module path -> replacement as written
}

// Package is one directory of Go files.
type Package struct {
	Path       string // import path
	Name       string // package name of the non-test files
	Dir        string // directory relative to the tree root
	Module     *Module
	GoFiles    []*File // non-test files: the package importers see
	TestFiles  []*File // _test.go files of the same package
	XTestFiles []*File // _test.go files of package <Name>_test
	Base       *Variant
	Test       *Variant
	XTest      *Variant
	checking   bool // set while Base is being type-checked, to detect import cycles
}

// File is one parsed .go file. It is parsed once and shared by every variant that includes it.
type File struct {
	Rel         string // path relative to the tree root, slash-separated
	Src         []byte
	AST         *ast.File
	Tok         *token.File
	Pkg         *Package
	Test        bool                               // the name ends in _test.go
	Guards      map[*ast.Ident]*ast.TypeSwitchStmt // x in `switch x := y.(type)`
	NamedIfaces map[*ast.InterfaceType]string      // interface literals that are a type declaration's type
}

// Variant is one type-checked build of a package, as `go test` builds them: "base" (the package
// alone, what importers see), "test" (plus its in-package _test.go files) or "xtest" (the external
// _test package, which imports the test variant of the package under test).
type Variant struct {
	Kind  string
	Pkg   *Package
	Path  string // import path it is checked under
	Files []*File
	Types *types.Package
	Info  *types.Info
}

// loadTree loads and type-checks every package of every module under root. Files named in
// overlay are read from it instead of the disk. Type errors are collected in Tree.Errors; any
// other problem (unparsable file, unsupported layout) is returned as an error.
func loadTree(label, root string, fset *token.FileSet, std types.ImporterFrom, overlay map[string][]byte) (*Tree, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	t := &Tree{
		Label: label, Root: abs, Fset: fset, std: std, overlay: overlay,
		byPath: map[string]*Package{}, ByRel: map[string]*File{}, byToken: map[*token.File]*File{},
		defIndex: map[*Variant]map[string]types.Object{},
	}
	t.ctx = moduleBuildContext
	t.ctx.OpenFile = func(name string) (io.ReadCloser, error) { // so build constraints see the overlay
		data, err := t.read(t.rel(name))
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	if err := t.findModules(); err != nil {
		return nil, err
	}
	if err := t.findPackages(); err != nil {
		return nil, err
	}
	if err := t.checkReplaces(); err != nil {
		return nil, err
	}
	t.typeCheck()
	return t, nil
}

func (t *Tree) rel(abs string) string {
	r, err := filepath.Rel(t.Root, abs)
	if err != nil || r == "." {
		return ""
	}
	return filepath.ToSlash(r)
}

func (t *Tree) abs(rel string) string { return filepath.Join(t.Root, filepath.FromSlash(rel)) }

func (t *Tree) read(rel string) ([]byte, error) {
	if data, ok := t.overlay[rel]; ok {
		return data, nil
	}
	return os.ReadFile(t.abs(rel))
}

// skipDir reports whether a directory is outside the build: review/ (old evidence copies of the
// code) at the tree root, and what the go command ignores (testdata, vendor, names starting with
// "." or "_").
func skipDir(rel, name string) bool {
	return rel == "review" || name == "testdata" || name == "vendor" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// findModules records every go.mod in the tree.
func (t *Tree) findModules() error {
	err := filepath.WalkDir(t.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel := t.rel(p)
		if rel != "" && skipDir(rel, d.Name()) {
			return filepath.SkipDir
		}
		data, err := t.read(path.Join(rel, "go.mod"))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		m, err := parseGoMod(data)
		if err != nil {
			return fmt.Errorf("%s: %v", path.Join(rel, "go.mod"), err)
		}
		m.Dir = rel
		if m.Toolchain != "" && m.Toolchain != runtime.Version() {
			return fmt.Errorf("%s asks for toolchain %s but the tool was built with %s",
				path.Join(rel, "go.mod"), m.Toolchain, runtime.Version())
		}
		t.Modules = append(t.Modules, m)
		return nil
	})
	if err != nil {
		return err
	}
	if len(t.Modules) == 0 {
		return fmt.Errorf("no go.mod under %s", t.Root)
	}
	return nil
}

// parseGoMod reads the few go.mod directives the tool needs. It is not a full go.mod parser: it
// understands module, go, toolchain and replace lines (single-line or in a block) and ignores the
// rest.
func parseGoMod(data []byte) (*Module, error) {
	m := &Module{Replace: map[string]string{}}
	block := ""
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			continue
		case block != "" && fields[0] == ")":
			block = ""
			continue
		case block != "":
			fields = append([]string{block}, fields...)
		case len(fields) == 2 && fields[1] == "(":
			block = fields[0]
			continue
		}
		switch fields[0] {
		case "module":
			if len(fields) != 2 {
				return nil, fmt.Errorf("malformed module line")
			}
			m.Path = unquoteMaybe(fields[1])
		case "go":
			if len(fields) != 2 {
				return nil, fmt.Errorf("malformed go line")
			}
			m.GoVersion = fields[1]
		case "toolchain":
			if len(fields) != 2 {
				return nil, fmt.Errorf("malformed toolchain line")
			}
			m.Toolchain = fields[1]
		case "replace":
			arrow := -1
			for i, f := range fields {
				if f == "=>" {
					arrow = i
				}
			}
			if arrow < 2 || arrow+1 >= len(fields) {
				return nil, fmt.Errorf("malformed replace line")
			}
			m.Replace[unquoteMaybe(fields[1])] = unquoteMaybe(fields[arrow+1])
		}
	}
	if m.Path == "" || m.GoVersion == "" {
		return nil, fmt.Errorf("missing module or go line")
	}
	return m, nil
}

func unquoteMaybe(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}

// findPackages walks each module's directories (skipping nested modules), selects the .go files
// the build context accepts, and parses them.
func (t *Tree) findPackages() error {
	isModuleDir := map[string]bool{}
	for _, m := range t.Modules {
		isModuleDir[m.Dir] = true
	}
	for _, m := range t.Modules {
		dirs := map[string][]string{} // relative dir -> .go file names
		err := filepath.WalkDir(t.abs(m.Dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := t.rel(p)
			if d.IsDir() {
				if rel != m.Dir && (isModuleDir[rel] || skipDir(rel, d.Name())) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".go") && d.Type().IsRegular() {
				dir := path.Dir(rel)
				if dir == "." {
					dir = ""
				}
				dirs[dir] = append(dirs[dir], d.Name())
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, dir := range sortedKeys(dirs) {
			if err := t.addPackage(m, dir, dirs[dir]); err != nil {
				return err
			}
		}
	}
	sort.Slice(t.Packages, func(i, j int) bool { return t.Packages[i].Path < t.Packages[j].Path })
	sort.Slice(t.Files, func(i, j int) bool { return t.Files[i].Rel < t.Files[j].Rel })
	sort.Strings(t.Excluded)
	return nil
}

func importPathOf(m *Module, dir string) string {
	if dir == m.Dir {
		return m.Path
	}
	sub := strings.TrimPrefix(strings.TrimPrefix(dir, m.Dir), "/")
	return m.Path + "/" + sub
}

func (t *Tree) addPackage(m *Module, dir string, names []string) error {
	sort.Strings(names)
	p := &Package{Path: importPathOf(m, dir), Dir: dir, Module: m}
	var tests []*File
	for _, name := range names {
		rel := path.Join(dir, name)
		ok, err := t.ctx.MatchFile(t.abs(dir), name)
		if err != nil {
			return fmt.Errorf("%s: %v", rel, err)
		}
		if !ok {
			t.Excluded = append(t.Excluded, rel)
			continue
		}
		f, err := t.parseFile(rel)
		if err != nil {
			return err
		}
		f.Pkg = p
		for _, imp := range f.AST.Imports {
			if imp.Path.Value == `"C"` {
				return fmt.Errorf("%s: cgo files are not supported", rel)
			}
		}
		if f.Test {
			tests = append(tests, f)
			continue
		}
		if p.Name == "" {
			p.Name = f.AST.Name.Name
		} else if f.AST.Name.Name != p.Name {
			return fmt.Errorf("%s: package %s, but %s holds package %s", rel, f.AST.Name.Name, dir, p.Name)
		}
		p.GoFiles = append(p.GoFiles, f)
	}
	if p.Name == "" && len(tests) > 0 { // a directory with test files only
		p.Name = strings.TrimSuffix(tests[0].AST.Name.Name, "_test")
		for _, f := range tests {
			if !strings.HasSuffix(f.AST.Name.Name, "_test") {
				p.Name = f.AST.Name.Name
			}
		}
	}
	for _, f := range tests {
		switch f.AST.Name.Name {
		case p.Name:
			p.TestFiles = append(p.TestFiles, f)
		case p.Name + "_test":
			p.XTestFiles = append(p.XTestFiles, f)
		default:
			return fmt.Errorf("%s: package %s in directory of package %s", f.Rel, f.AST.Name.Name, p.Name)
		}
	}
	if len(p.GoFiles)+len(p.TestFiles)+len(p.XTestFiles) == 0 {
		return nil // every file was excluded by the build context
	}
	t.Packages = append(t.Packages, p)
	t.byPath[p.Path] = p
	return nil
}

// parseFile parses one file (once per tree) and indexes what later stages need: type-switch guards
// and the interface literals that are the type of a type declaration.
func (t *Tree) parseFile(rel string) (*File, error) {
	src, err := t.read(rel)
	if err != nil {
		return nil, err
	}
	af, err := parser.ParseFile(t.Fset, rel, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	f := &File{
		Rel: rel, Src: src, AST: af, Tok: t.Fset.File(af.FileStart),
		Test:   strings.HasSuffix(rel, "_test.go"),
		Guards: map[*ast.Ident]*ast.TypeSwitchStmt{}, NamedIfaces: map[*ast.InterfaceType]string{},
	}
	ast.Inspect(af, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.TypeSwitchStmt:
			if as, ok := n.Assign.(*ast.AssignStmt); ok && len(as.Lhs) == 1 {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					f.Guards[id] = n
				}
			}
		case *ast.TypeSpec:
			if it, ok := n.Type.(*ast.InterfaceType); ok {
				f.NamedIfaces[it] = n.Name.Name
			}
		}
		return true
	})
	t.Files = append(t.Files, f)
	t.ByRel[rel] = f
	t.byToken[f.Tok] = f
	return f, nil
}

// checkReplaces makes sure that when one module of the tree imports a package of another module
// of the tree, its go.mod replaces that module with the tree's copy (as ui/go.mod does with
// `replace github.com/eval-apply-labs/go-ledger => ../`). Otherwise `go build` would use a
// downloaded version, not the code the tool checks.
func (t *Tree) checkReplaces() error {
	for _, p := range t.Packages {
		for _, f := range p.allFiles() {
			for _, imp := range f.AST.Imports {
				q := t.byPath[unquoteMaybe(imp.Path.Value)]
				if q == nil || q.Module == p.Module {
					continue
				}
				want, err := filepath.Rel(t.abs(p.Module.Dir), t.abs(q.Module.Dir))
				if err != nil {
					return err
				}
				got, ok := p.Module.Replace[q.Module.Path]
				if !ok || filepath.Clean(filepath.FromSlash(got)) != want {
					return fmt.Errorf("%s imports %s from module %s, but %s does not replace that module with %s",
						f.Rel, q.Path, q.Module.Path, path.Join(p.Module.Dir, "go.mod"), want)
				}
			}
		}
	}
	return nil
}

func (p *Package) allFiles() []*File {
	var all []*File
	all = append(all, p.GoFiles...)
	all = append(all, p.TestFiles...)
	return append(all, p.XTestFiles...)
}

// variants returns the package's type-checked variants in a fixed order.
func (p *Package) variants() []*Variant {
	var vs []*Variant
	for _, v := range []*Variant{p.Base, p.Test, p.XTest} {
		if v != nil {
			vs = append(vs, v)
		}
	}
	return vs
}

// typeCheck checks every package's base variant (dependencies first, through the importer), then
// the test and xtest variants.
func (t *Tree) typeCheck() {
	for _, p := range t.Packages {
		if len(p.GoFiles) > 0 {
			if _, err := t.checkBase(p); err != nil {
				t.Errors = append(t.Errors, err.Error())
			}
		}
	}
	for _, p := range t.Packages {
		if len(p.TestFiles) > 0 {
			files := append(append([]*File(nil), p.GoFiles...), p.TestFiles...)
			p.Test = t.check(p, "test", p.Path, files, nil)
		}
		if len(p.XTestFiles) > 0 {
			override := map[string]*types.Package{}
			if p.Test != nil {
				override[p.Path] = p.Test.Types
			} else if p.Base != nil {
				override[p.Path] = p.Base.Types
			}
			p.XTest = t.check(p, "xtest", p.Path+"_test", p.XTestFiles, override)
		}
	}
}

// checkBase returns the base variant of p, type-checking it on first use.
func (t *Tree) checkBase(p *Package) (*types.Package, error) {
	if p.Base != nil {
		return p.Base.Types, nil
	}
	if p.checking {
		return nil, fmt.Errorf("import cycle through %s", p.Path)
	}
	if len(p.GoFiles) == 0 {
		return nil, fmt.Errorf("package %s has no non-test Go files", p.Path)
	}
	p.checking = true
	v := t.check(p, "base", p.Path, p.GoFiles, nil)
	p.checking = false
	p.Base = v
	return v.Types, nil
}

func (t *Tree) check(p *Package, kind, importPath string, files []*File, override map[string]*types.Package) *Variant {
	v := &Variant{Kind: kind, Pkg: p, Path: importPath, Files: files, Info: &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
		Instances:  map[*ast.Ident]types.Instance{},
	}}
	conf := types.Config{
		GoVersion: "go" + p.Module.GoVersion,
		Importer:  &moduleImporter{tree: t, override: override},
		Sizes:     types.SizesFor("gc", t.ctx.GOARCH),
		Error: func(err error) {
			t.Errors = append(t.Errors, fmt.Sprintf("%v [%s variant of %s]", err, kind, p.Path))
		},
	}
	asts := make([]*ast.File, len(files))
	for i, f := range files {
		asts[i] = f.AST
	}
	v.Types, _ = conf.Check(importPath, t.Fset, asts, v.Info) // errors go to conf.Error
	t.Variants = append(t.Variants, v)
	return v
}

// moduleImporter resolves imports for one variant: the tree's own packages to their base variant
// (or, through override, the xtest's package under test to its test variant), the standard library
// to the shared source importer, and anything else to an error (there are no third-party modules).
type moduleImporter struct {
	tree     *Tree
	override map[string]*types.Package
}

func (m *moduleImporter) Import(path string) (*types.Package, error) {
	return m.ImportFrom(path, "", 0)
}

func (m *moduleImporter) ImportFrom(importPath, dir string, mode types.ImportMode) (*types.Package, error) {
	if pkg, ok := m.override[importPath]; ok {
		return pkg, nil
	}
	if p := m.tree.byPath[importPath]; p != nil {
		return m.tree.checkBase(p)
	}
	if !isStandard(importPath) {
		return nil, fmt.Errorf("%q is neither in the tree nor in the standard library", importPath)
	}
	return m.tree.std.ImportFrom(importPath, "", 0)
}

// isStandard reports whether importPath names a standard-library package of the tool's GOROOT.
func isStandard(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	if strings.Contains(first, ".") || importPath == "C" {
		return false
	}
	if importPath == "unsafe" {
		return true
	}
	info, err := os.Stat(filepath.Join(build.Default.GOROOT, "src", filepath.FromSlash(importPath)))
	return err == nil && info.IsDir()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
