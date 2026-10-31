package main

// check proves that a new tree is an old tree with only names changed:
//
//  1. Files. The same .go files exist; every other file outside review/, except Markdown, is
//     byte-identical (go.mod, ui/templates/, Makefile...); .go files the build leaves out are too.
//  2. Types. Both trees type-check with zero errors, in all three variants of every package.
//  3. Syntax. Each pair of files has the same AST, compared field by field by reflection, ignoring
//     only positions and comments: node types, operators, tokens and literals must match, so every
//     string literal is unchanged. Build directives (//go:build, //go:embed...) must match too,
//     and so must comments inside Example functions, where go test reads the expected output.
//  4. Identifiers. Each identifier resolves, on both sides, to what it declares and uses. Objects
//     declared in the tree must correspond one to one (old key <-> new key, same kind) across all
//     files and variants; anything else (standard library, universe, imported package names) must
//     be the very same object. This is what catches capture and shadowing. Selections x.f must
//     also take the same path through embedded fields, and package initialization order must match.
//  5. JSON. Each field keeps its effective JSON key and options, and other tag keys are unchanged;
//     and each struct's JSON members, as encoding/json resolves them, are unchanged.
//  6. Method sets. For every named type of the tree (T and *T, plus the instantiations of generic
//     types the code uses) and every interface that appears in the tree or is declared by an
//     imported package, types.Implements gives the same answer, and when it holds each interface
//     method is implemented by corresponding methods (so swapping two methods' names is caught).
//  7. Names the toolchain or the standard library look up by name keep them: main, init and
//     TestMain; Test/Benchmark/Example/Fuzz prefixes; methods checked dynamically; fields and
//     methods used by the templates; exportedness everywhere.
//  8. Fail closed. If a field or method was renamed and the code looks names up by reflection or
//     parses templates from strings or run-time files, or if a .go file excluded by the build
//     constraints mentions a renamed name, the check fails: the proof does not cover those.

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/scanner"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pair is one object of the old tree and its counterpart in the new tree.
type Pair struct {
	OldKey, NewKey   string
	OldName, NewName string
	Kind             string
	oldObj, newObj   types.Object // representatives (nil for a type-switch guard)
	oldFile          *File
}

// Result is the outcome of comparing two loaded trees.
type Result struct {
	Violations  []string
	Warnings    []string          // things the proof does not cover; they do not fail the check
	Pairs       map[string]*Pair  // by old key
	newToOld    map[string]string // new key -> old key
	Files       int               // files compared
	Identifiers int               // identifier pairs compared (counting each variant)
}

func runCheck(args []string) int {
	flags := flag.NewFlagSet("check", flag.ExitOnError)
	table := flags.String("table", "", "also write a markdown table of renamed non-local objects here")
	flags.Parse(args)
	if flags.NArg() != 2 {
		usage()
	}
	fset := token.NewFileSet()
	std := newStdImporter(fset)
	oldTree, err := loadTree("old", flags.Arg(0), fset, std, nil)
	if err != nil {
		fatalf("old tree: %v", err)
	}
	newTree, err := loadTree("new", flags.Arg(1), fset, std, nil)
	if err != nil {
		fatalf("new tree: %v", err)
	}
	res := compareTrees(oldTree, newTree)
	res.Violations = append(compareDisk(oldTree, newTree), res.Violations...)

	fmt.Printf("compared %d .go files (%d identifier occurrences across variants) in %d packages\n",
		res.Files, res.Identifiers, len(oldTree.Packages))
	printRenameSummary(os.Stdout, res)
	if *table != "" {
		if err := os.WriteFile(*table, []byte(renameTable(oldTree, newTree, res)), 0o644); err != nil {
			fatalf("%v", err)
		}
		fmt.Printf("wrote %s\n", *table)
	}
	for _, w := range res.Warnings {
		fmt.Println("WARNING", w)
	}
	if len(res.Violations) > 0 {
		for _, v := range res.Violations {
			fmt.Println("VIOLATION", v)
		}
		fmt.Printf("check FAILED: %d violations\n", len(res.Violations))
		return 1
	}
	fmt.Println("check OK: the new tree is the old program with only names changed")
	return 0
}

// compareDisk compares the two trees' files (outside review/): the same files must exist, except
// Markdown; and every file that is not a type-checked .go file must be byte-identical, except
// Markdown, which comment and documentation fixes may change.
func compareDisk(old, new *Tree) []string {
	list := func(t *Tree) (map[string]bool, error) {
		files := map[string]bool{}
		err := filepath.WalkDir(t.Root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := t.rel(p)
			if d.IsDir() {
				if rel == "review" || rel == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if rel == ".git" { // a git worktree's link file (gitdir: ...), not part of the tree
				return nil
			}
			files[rel] = true
			return nil
		})
		return files, err
	}
	var out []string
	oldFiles, err1 := list(old)
	newFiles, err2 := list(new)
	if err1 != nil || err2 != nil {
		return []string{fmt.Sprintf("cannot list the trees: %v %v", err1, err2)}
	}
	all := map[string]bool{}
	for f := range oldFiles {
		all[f] = true
	}
	for f := range newFiles {
		all[f] = true
	}
	for _, rel := range sortedKeys(all) {
		isMarkdown := path.Ext(rel) == ".md"
		switch {
		case !oldFiles[rel] && !isMarkdown:
			out = append(out, rel+": file only in the new tree")
		case !newFiles[rel] && !isMarkdown:
			out = append(out, rel+": file missing from the new tree")
		case isMarkdown || !oldFiles[rel] || !newFiles[rel]:
		case path.Ext(rel) == ".go" && old.ByRel[rel] != nil:
			// compared semantically by compareTrees
		default:
			a, errA := os.ReadFile(old.abs(rel))
			b, errB := os.ReadFile(new.abs(rel))
			if errA != nil || errB != nil || !bytes.Equal(a, b) {
				out = append(out, rel+": must be byte-identical (not a type-checked .go file) but differs")
			}
		}
	}
	return out
}

// comparer holds the state of one tree comparison.
type comparer struct {
	old, new  *Tree
	res       *Result
	reported  map[string]bool
	templates map[string]string

	// the variant pair and file pair being walked
	ov, nv *Variant
	fo, fn *File

	ifaces    []ifacePair
	structs   []structPair
	types     []typePair
	instances map[string]bool // instantiated generic types already in types
	warned    map[string]bool
}

type ifacePair struct {
	old, new *types.Interface
	label    string
}

type structPair struct {
	old, new       *types.Struct
	oldPos, newPos token.Pos
}

type typePair struct {
	old, new *types.Named
	label    string
}

// compareTrees runs every semantic comparison between two loaded trees (everything except the
// on-disk comparison of non-Go files, which runCheck adds).
func compareTrees(old, new *Tree) *Result {
	c := &comparer{old: old, new: new, reported: map[string]bool{}, instances: map[string]bool{}, warned: map[string]bool{},
		res: &Result{Pairs: map[string]*Pair{}, newToOld: map[string]string{}}}
	for _, e := range old.Errors {
		c.report("old tree: type error: " + e)
	}
	for _, e := range new.Errors {
		c.report("new tree: type error: " + e)
	}
	templates, err := templateFieldNames(old)
	if err != nil {
		c.report("cannot read the templates: " + err.Error())
	}
	c.templates = templates
	c.comparePackages()
	c.compareDirectives()
	c.checkNames()
	c.compareJSON()
	c.compareMethodSets()
	c.nameLookupGuards()
	sort.Strings(c.res.Violations)
	sort.Strings(c.res.Warnings)
	return c.res
}

func (c *comparer) warn(msg string) {
	if !c.warned[msg] {
		c.warned[msg] = true
		c.res.Warnings = append(c.res.Warnings, msg)
	}
}

func (c *comparer) report(msg string) {
	if !c.reported[msg] {
		c.reported[msg] = true
		c.res.Violations = append(c.res.Violations, msg)
	}
}

// violation reports a problem at a pair of positions: the new tree's first, then the old tree's.
func (c *comparer) violation(newPos, oldPos token.Pos, format string, args ...any) {
	c.report(fmt.Sprintf("%s (old %s): %s", c.new.position(newPos), c.old.position(oldPos), fmt.Sprintf(format, args...)))
}

func (c *comparer) comparePackages() {
	for _, op := range c.old.Packages {
		np := c.new.byPath[op.Path]
		if np == nil {
			c.report(fmt.Sprintf("package %s: missing from the new tree", op.Path))
			continue
		}
		if op.Name != np.Name || op.Dir != np.Dir {
			c.report(fmt.Sprintf("package %s: name or directory changed (%s in %s -> %s in %s)",
				op.Path, op.Name, op.Dir, np.Name, np.Dir))
			continue
		}
		c.compareVariant(op, op.Base, np.Base)
		c.compareVariant(op, op.Test, np.Test)
		c.compareVariant(op, op.XTest, np.XTest)
	}
	for _, np := range c.new.Packages {
		if c.old.byPath[np.Path] == nil {
			c.report(fmt.Sprintf("package %s: only in the new tree", np.Path))
		}
	}
	c.res.Files = 0
	for _, f := range c.old.Files {
		if c.new.ByRel[f.Rel] != nil {
			c.res.Files++
		}
	}
}

func (c *comparer) compareVariant(p *Package, ov, nv *Variant) {
	if ov == nil || nv == nil {
		if ov != nil || nv != nil {
			c.report(fmt.Sprintf("package %s: a variant exists in one tree only", p.Path))
		}
		return
	}
	if len(ov.Files) != len(nv.Files) {
		c.report(fmt.Sprintf("package %s (%s variant): %d files became %d", p.Path, ov.Kind, len(ov.Files), len(nv.Files)))
		return
	}
	c.ov, c.nv = ov, nv
	for i := range ov.Files {
		c.fo, c.fn = ov.Files[i], nv.Files[i]
		if c.fo.Rel != c.fn.Rel {
			c.report(fmt.Sprintf("package %s (%s variant): file %s became %s", p.Path, ov.Kind, c.fo.Rel, c.fn.Rel))
			continue
		}
		c.node(c.fo.AST, c.fn.AST)
	}
	c.collectTypes()
	c.compareInitOrder()
}

// compareInitOrder checks that package-level variables are initialized in the same order. (The
// order follows from references and declaration order, which the other checks already fix; this
// compares go/types' own computation of it.)
func (c *comparer) compareInitOrder() {
	oi, ni := c.ov.Info.InitOrder, c.nv.Info.InitOrder
	if len(oi) != len(ni) {
		c.report(fmt.Sprintf("package %s (%s variant): %d package initializers became %d", c.ov.Path, c.ov.Kind, len(oi), len(ni)))
		return
	}
	for i := range oi {
		same := len(oi[i].Lhs) == len(ni[i].Lhs)
		for j := 0; same && j < len(oi[i].Lhs); j++ {
			ov, nv := oi[i].Lhs[j], ni[i].Lhs[j]
			if ov.Name() == "_" || nv.Name() == "_" {
				same = ov.Name() == nv.Name()
				continue
			}
			p := c.res.Pairs[c.old.keyOf(ov)]
			same = p != nil && p.NewKey == c.new.keyOf(nv)
		}
		if !same {
			c.report(fmt.Sprintf("package %s (%s variant): package initialization order changed at %s", c.ov.Path, c.ov.Kind, oi[i]))
			return
		}
	}
}

var (
	posType          = reflect.TypeFor[token.Pos]()
	commentGroupType = reflect.TypeFor[*ast.CommentGroup]()
	objectType       = reflect.TypeFor[*ast.Object]()
	scopeType        = reflect.TypeFor[*ast.Scope]()
)

// node compares two AST nodes of the same dynamic type.
func (c *comparer) node(o, n ast.Node) {
	switch o := o.(type) {
	case *ast.File:
		n := n.(*ast.File)
		if o.Name.Name != n.Name.Name {
			c.violation(n.Name.Pos(), o.Name.Pos(), "package clause changed: %s -> %s", o.Name.Name, n.Name.Name)
		}
		if o.GoVersion != n.GoVersion {
			c.violation(n.Pos(), o.Pos(), "file Go version changed")
		}
		// Only Decls: Imports repeats the import declarations, and the rest is positions,
		// comments and the parser's deprecated object resolution.
		c.value(reflect.ValueOf(o.Decls), reflect.ValueOf(n.Decls), o, n)
		return
	case *ast.Ident:
		c.ident(o, n.(*ast.Ident))
		return
	case *ast.BasicLit:
		n := n.(*ast.BasicLit)
		if o.Kind != n.Kind || o.Value != n.Value {
			c.violation(n.Pos(), o.Pos(), "literal changed: %s -> %s", o.Value, n.Value)
		}
		if o.Kind == token.STRING && !c.fo.Test && strings.Contains(o.Value, "%") {
			for _, verb := range []string{"%T", "%+v", "%#v"} {
				if strings.Contains(o.Value, verb) {
					c.warn(fmt.Sprintf("%s: format %s prints Go type or field names, which renames change in the output",
						c.old.keyAt(o.Pos()), verb))
				}
			}
		}
		return
	case *ast.StructType:
		c.structType(o, n.(*ast.StructType))
		return
	case *ast.InterfaceType:
		c.recordInterface(o, n.(*ast.InterfaceType)) // then compared like any node
	case *ast.SelectorExpr:
		c.compareSelection(o, n.(*ast.SelectorExpr)) // then compared like any node
	case *ast.Comment, *ast.CommentGroup:
		return
	}
	c.fields(reflect.ValueOf(o).Elem(), reflect.ValueOf(n).Elem(), o, n)
}

// compareSelection checks that x.f selects the same way: same kind (field, method value, method
// expression), same path of field indices through embedded fields, same implicit indirections.
// The selected object itself is compared as an identifier.
func (c *comparer) compareSelection(o, n *ast.SelectorExpr) {
	oldSel, newSel := c.ov.Info.Selections[o], c.nv.Info.Selections[n]
	switch {
	case oldSel == nil && newSel == nil: // a qualified identifier (pkg.Name)
	case oldSel == nil || newSel == nil:
		c.violation(n.Pos(), o.Pos(), "selector %s changed between a qualified identifier and a selection", n.Sel.Name)
	case oldSel.Kind() != newSel.Kind() || oldSel.Indirect() != newSel.Indirect() || !equalInts(oldSel.Index(), newSel.Index()):
		c.violation(n.Pos(), o.Pos(), "selection of %s takes a different path: %v -> %v", n.Sel.Name, oldSel.Index(), newSel.Index())
	}
}

// fields compares every field of two AST node structs, except positions and comments.
func (c *comparer) fields(ov, nv reflect.Value, o, n ast.Node) {
	for i := 0; i < ov.NumField(); i++ {
		switch ov.Type().Field(i).Type {
		case posType, commentGroupType, objectType, scopeType:
			continue
		}
		c.value(ov.Field(i), nv.Field(i), o, n)
	}
}

// value compares two values found in AST nodes. o and n are the nearest enclosing nodes, for
// messages.
func (c *comparer) value(ov, nv reflect.Value, o, n ast.Node) {
	switch ov.Kind() {
	case reflect.Pointer, reflect.Interface:
		if ov.IsNil() || nv.IsNil() {
			if ov.IsNil() != nv.IsNil() {
				c.violation(n.Pos(), o.Pos(), "syntax changed: part of a %T is present on one side only", o)
			}
			return
		}
		if ov.Elem().Type() != nv.Elem().Type() {
			c.violation(n.Pos(), o.Pos(), "syntax changed: %s became %s", ov.Elem().Type(), nv.Elem().Type())
			return
		}
		if on, ok := ov.Interface().(ast.Node); ok {
			c.node(on, nv.Interface().(ast.Node))
			return
		}
		c.value(ov.Elem(), nv.Elem(), o, n)
	case reflect.Slice:
		if ov.Len() != nv.Len() {
			c.violation(n.Pos(), o.Pos(), "syntax changed: %d elements became %d in a %T", ov.Len(), nv.Len(), o)
			return
		}
		for i := 0; i < ov.Len(); i++ {
			c.value(ov.Index(i), nv.Index(i), o, n)
		}
	case reflect.Struct:
		c.fields(ov, nv, o, n)
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !ov.Equal(nv) {
			c.violation(n.Pos(), o.Pos(), "syntax changed in a %T: %v -> %v", o, ov.Interface(), nv.Interface())
		}
	default:
		c.violation(n.Pos(), o.Pos(), "cannot compare a %s in a %T", ov.Kind(), o)
	}
}

// structType compares a struct type's fields; their tags are compared by effect in compareJSON.
func (c *comparer) structType(o, n *ast.StructType) {
	if len(o.Fields.List) != len(n.Fields.List) {
		c.violation(n.Pos(), o.Pos(), "struct has %d field declarations, was %d", len(n.Fields.List), len(o.Fields.List))
		return
	}
	for i, of := range o.Fields.List {
		nf := n.Fields.List[i]
		if len(of.Names) != len(nf.Names) {
			c.violation(nf.Pos(), of.Pos(), "field declaration has %d names, was %d", len(nf.Names), len(of.Names))
			continue
		}
		for j := range of.Names {
			c.ident(of.Names[j], nf.Names[j])
		}
		c.value(reflect.ValueOf(&of.Type).Elem(), reflect.ValueOf(&nf.Type).Elem(), of, nf)
	}
	oldStruct, _ := c.ov.Info.TypeOf(o).(*types.Struct)
	newStruct, _ := c.nv.Info.TypeOf(n).(*types.Struct)
	if oldStruct == nil || newStruct == nil {
		c.violation(n.Pos(), o.Pos(), "struct type was not type-checked")
		return
	}
	c.structs = append(c.structs, structPair{oldStruct, newStruct, o.Pos(), n.Pos()})
}

func (c *comparer) recordInterface(o, n *ast.InterfaceType) {
	oi, _ := c.ov.Info.TypeOf(o).(*types.Interface)
	ni, _ := c.nv.Info.TypeOf(n).(*types.Interface)
	if oi == nil || ni == nil {
		c.violation(n.Pos(), o.Pos(), "interface type was not type-checked")
		return
	}
	label := "interface literal at " + c.old.keyAt(o.Pos())
	if name, ok := c.fo.NamedIfaces[o]; ok {
		label = c.ov.Path + "." + name
	}
	c.ifaces = append(c.ifaces, ifacePair{oi, ni, label})
}

// ident compares one pair of identifiers by what they resolve to.
func (c *comparer) ident(o, n *ast.Ident) {
	c.res.Identifiers++
	if o.Name == "_" || n.Name == "_" {
		if o.Name != n.Name {
			c.violation(n.Pos(), o.Pos(), "blank identifier changed: %s -> %s", o.Name, n.Name)
		}
		return
	}
	od, ou := c.old.resolve(c.ov, c.fo, o)
	nd, nu := c.new.resolve(c.nv, c.fn, n)
	if od.none() && ou.none() && nd.none() && nu.none() {
		if o.Name != n.Name {
			c.violation(n.Pos(), o.Pos(), "unresolved identifier changed: %s -> %s", o.Name, n.Name)
		}
		return
	}
	c.sameRef(od, nd, o, n, "declares")
	c.sameRef(ou, nu, o, n, "refers to")

	// An instantiation of a generic type of the tree (Set[int]) is a concrete type whose method
	// set compareMethodSets must check too; generic declarations themselves are not checkable.
	if oi, ok := c.ov.Info.Instances[o]; ok {
		ni, ok := c.nv.Info.Instances[n]
		on, ook := oi.Type.(*types.Named)
		nn, nok := ni.Type.(*types.Named)
		if ok && ook && nok && c.old.keyOf(on.Origin().Obj()) != "" {
			label := types.TypeString(on, qualifier(nil))
			if seen := c.ov.Kind + " " + c.ov.Path + " " + label; !c.instances[seen] {
				c.instances[seen] = true
				c.types = append(c.types, typePair{on, nn, label})
			}
		}
	}
}

func (c *comparer) describe(t *Tree, r ref) string {
	switch {
	case r.none():
		return "nothing"
	case r.key != "" && r.obj == nil:
		return "the type-switch variable declared at " + r.key
	case r.key != "":
		return fmt.Sprintf("%s %s declared at %s", kindOf(r.obj), r.obj.Name(), r.key)
	}
	if pn, ok := r.obj.(*types.PkgName); ok {
		return fmt.Sprintf("imported package %s (%q)", pn.Name(), pn.Imported().Path())
	}
	if r.obj.Pkg() == nil {
		return "predeclared " + r.obj.Name()
	}
	return fmt.Sprintf("%s %s.%s", kindOf(r.obj), r.obj.Pkg().Path(), r.obj.Name())
}

// sameRef checks that two identifiers declare (or refer to) corresponding objects.
func (c *comparer) sameRef(or, nr ref, o, n *ast.Ident, what string) {
	switch {
	case or.none() && nr.none():
		return
	case or.key != "" && nr.key != "":
		c.pair(or, nr, o, n)
	case or.key == "" && nr.key == "" && !or.none() && !nr.none():
		if !sameExternal(or.obj, nr.obj) {
			c.violation(n.Pos(), o.Pos(), "%s %s %s, was %s", n.Name, what, c.describe(c.new, nr), c.describe(c.old, or))
		}
	case or.none() || nr.none():
		c.violation(n.Pos(), o.Pos(), "%s %s %s, was %s", n.Name, what, c.describe(c.new, nr), c.describe(c.old, or))
	default: // one side is an object of the tree, the other is not
		c.violation(n.Pos(), o.Pos(), "%s %s %s, was %s (capture or shadowing)", n.Name, what,
			c.describe(c.new, nr), c.describe(c.old, or))
	}
}

// sameExternal reports whether two objects declared outside the trees are the same: package
// names by name and imported path, everything else by identity (both trees share one
// standard-library importer and the universe).
func sameExternal(a, b types.Object) bool {
	if pa, ok := a.(*types.PkgName); ok {
		pb, ok := b.(*types.PkgName)
		return ok && pa.Name() == pb.Name() && pa.Imported().Path() == pb.Imported().Path()
	}
	return origin(a) == origin(b)
}

// pair records that old object or corresponds to new object nr and checks the correspondence
// stays one to one.
func (c *comparer) pair(or, nr ref, o, n *ast.Ident) {
	if p, ok := c.res.Pairs[or.key]; ok {
		if p.NewKey != nr.key {
			c.violation(n.Pos(), o.Pos(), "%s refers to %s, but the old object (%s) corresponds to %s elsewhere",
				n.Name, c.describe(c.new, nr), c.describe(c.old, or), p.NewKey)
		}
		if p.oldObj == nil && or.obj != nil {
			p.oldObj, p.newObj = or.obj, nr.obj
		}
		return
	}
	if prev, ok := c.res.newToOld[nr.key]; ok && prev != or.key {
		c.violation(n.Pos(), o.Pos(), "%s refers to %s, which already corresponds to the old object at %s, not %s (capture)",
			n.Name, c.describe(c.new, nr), prev, c.describe(c.old, or))
		return
	}
	p := &Pair{OldKey: or.key, NewKey: nr.key, Kind: kindOf(or.obj), oldObj: or.obj, newObj: nr.obj, oldFile: c.old.fileAt(c.old.posOfKey(or.key))}
	p.OldName, p.NewName = nameAtKey(c.old, or), nameAtKey(c.new, nr)
	if nk := kindOf(nr.obj); nk != p.Kind {
		c.violation(n.Pos(), o.Pos(), "%s is a %s, was a %s", n.Name, nk, p.Kind)
	}
	if ov, ok := or.obj.(*types.Var); ok {
		if nv, ok := nr.obj.(*types.Var); ok && ov.Embedded() != nv.Embedded() {
			c.violation(n.Pos(), o.Pos(), "%s: embedded field status changed", n.Name)
		}
	}
	if ot, ok := or.obj.(*types.TypeName); ok {
		if nt, ok := nr.obj.(*types.TypeName); ok && ot.IsAlias() != nt.IsAlias() {
			c.violation(n.Pos(), o.Pos(), "%s: alias status changed", n.Name)
		}
	}
	c.res.Pairs[or.key] = p
	c.res.newToOld[nr.key] = or.key
}

func nameAtKey(t *Tree, r ref) string {
	if r.obj != nil {
		return r.obj.Name()
	}
	pos := t.posOfKey(r.key)
	f := t.fileAt(pos)
	if f == nil {
		return "?"
	}
	off := f.Tok.Offset(pos)
	end := off
	for end < len(f.Src) && (f.Src[end] == '_' || unicode.IsLetter(rune(f.Src[end])) || unicode.IsDigit(rune(f.Src[end])) || f.Src[end] >= utf8.RuneSelf) {
		end++
	}
	return string(f.Src[off:end])
}

// compareDirectives compares the comment lines that the toolchain reads (//go:build, //go:embed,
// //go:generate, //line, // +build, ...), which the AST comparison ignores as comments.
func (c *comparer) compareDirectives() {
	for _, of := range c.old.Files {
		nf := c.new.ByRel[of.Rel]
		if nf == nil {
			continue
		}
		od, nd := directives(of.AST), directives(nf.AST)
		if strings.Join(od, "\n") != strings.Join(nd, "\n") {
			c.report(fmt.Sprintf("%s: build directives changed: %q -> %q", of.Rel, od, nd))
		}
		if of.Test {
			oe, ne := exampleComments(of.AST), exampleComments(nf.AST)
			if strings.Join(oe, "\n") != strings.Join(ne, "\n") {
				c.report(fmt.Sprintf("%s: comments inside Example functions changed (go test compares their // Output:)", of.Rel))
			}
		}
	}
}

// exampleComments returns the comments inside the bodies of a test file's Example functions,
// where go test reads the expected output.
func exampleComments(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Body == nil || testKind(fd.Name.Name) != "Example" {
			continue
		}
		for _, group := range f.Comments {
			if group.Pos() > fd.Body.Lbrace && group.End() <= fd.Body.Rbrace {
				out = append(out, group.Text())
			}
		}
	}
	return out
}

func directives(f *ast.File) []string {
	var out []string
	for _, group := range f.Comments {
		for _, comment := range group.List {
			text := comment.Text
			if strings.HasPrefix(text, "//go:") || strings.HasPrefix(text, "// +build") ||
				strings.HasPrefix(text, "//line ") || strings.HasPrefix(text, "/*line ") ||
				strings.HasPrefix(text, "//export ") || strings.HasPrefix(text, "//extern ") {
				out = append(out, strings.TrimSpace(text))
			}
		}
	}
	return out
}

// checkNames enforces the rules for names that something looks up by name.
func (c *comparer) checkNames() {
	for _, key := range sortedKeys(c.res.Pairs) {
		p := c.res.Pairs[key]
		if p.OldName == p.NewName {
			continue
		}
		at := func(format string, args ...any) {
			c.violation(c.new.posOfKey(p.NewKey), c.old.posOfKey(p.OldKey), "%s -> %s: %s", p.OldName, p.NewName, fmt.Sprintf(format, args...))
		}
		if ast.IsExported(p.OldName) != ast.IsExported(p.NewName) && !localKinds[p.Kind] && !isFunctionLocal(p.oldObj) {
			at("exportedness changed")
		}
		switch p.Kind {
		case "package func":
			if entryPoints[p.OldName] || entryPoints[p.NewName] {
				at("main, init and TestMain are called by name")
			}
			if p.oldFile != nil && p.oldFile.Test && testKind(p.OldName) != testKind(p.NewName) {
				at("a Test/Benchmark/Example/Fuzz function must keep its prefix (go test finds it by name)")
			}
		case "method":
			if dynamicNames[p.OldName] || dynamicNames[p.NewName] {
				at("the standard library looks for this method name at run time")
			}
		}
		if (p.Kind == "method" || p.Kind == "struct field") && ast.IsExported(p.OldName) {
			if where, ok := c.templates[p.OldName]; ok {
				at("the templates use .%s (%s)", p.OldName, where)
			} else if where, ok := c.templates[p.NewName]; ok {
				at("the templates use .%s (%s)", p.NewName, where)
			}
		}
	}
}

// testKind returns the go test prefix a function name has ("Test", "Benchmark", "Example",
// "Fuzz"), or "". As in go test, the prefix counts only when the next rune is not lower case.
func testKind(name string) string {
	for _, prefix := range []string{"Test", "Benchmark", "Example", "Fuzz"} {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if len(name) == len(prefix) {
			return prefix
		}
		if r, _ := utf8.DecodeRuneInString(name[len(prefix):]); !unicode.IsLower(r) {
			return prefix
		}
	}
	return ""
}

// compareJSON compares every struct's fields by their JSON effect and every struct's JSON members.
func (c *comparer) compareJSON() {
	type seenKey struct{ o, n *types.Struct }
	seen := map[seenKey]bool{}
	for _, sp := range c.structs {
		if seen[seenKey{sp.old, sp.new}] || sp.old.NumFields() != sp.new.NumFields() {
			continue
		}
		seen[seenKey{sp.old, sp.new}] = true
		for i := 0; i < sp.old.NumFields(); i++ {
			of, nf := sp.old.Field(i), sp.new.Field(i)
			ot, nt := sp.old.Tag(i), sp.new.Tag(i)
			ov, nv := jsonViewOf(of, ot).effective(), jsonViewOf(nf, nt).effective()
			if ov != nv {
				c.violation(nf.Pos(), of.Pos(), "field %s -> %s: JSON changed: %s -> %s", of.Name(), nf.Name(), describeJSON(ov), describeJSON(nv))
			}
			if otherTagKeys(ot) != otherTagKeys(nt) {
				c.violation(nf.Pos(), of.Pos(), "field %s: non-JSON struct tag keys changed: %q -> %q", nf.Name(), ot, nt)
			}
		}
		om, nm := jsonMembers(sp.old), jsonMembers(sp.new)
		if !sameMembers(om, nm) {
			c.violation(sp.newPos, sp.oldPos, "struct's JSON members changed: [%s] -> [%s]", memberKeys(om), memberKeys(nm))
		}
	}
}

func describeJSON(v jsonView) string {
	switch {
	case v.Ignored:
		return "not serialized"
	case v.Flattened:
		return "embedded struct, members promoted"
	}
	return fmt.Sprintf("key %q%s", v.Key, v.Options)
}

// collectTypes pairs the named types the current variant pair declares, through the key mapping.
func (c *comparer) collectTypes() {
	oldDefs, newDefs := c.old.defsByKey(c.ov), c.new.defsByKey(c.nv)
	for _, key := range sortedKeys(oldDefs) {
		tn, ok := oldDefs[key].(*types.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}
		on, ok := tn.Type().(*types.Named)
		if !ok || on.TypeParams().Len() > 0 {
			continue
		}
		p := c.res.Pairs[key]
		if p == nil {
			continue
		}
		ntn, _ := newDefs[p.NewKey].(*types.TypeName)
		if ntn == nil {
			continue
		}
		if nn, ok := ntn.Type().(*types.Named); ok {
			c.types = append(c.types, typePair{on, nn, c.ov.Path + "." + tn.Name()})
		}
	}
}

// compareMethodSets checks that every named type of the tree implements exactly the same
// interfaces as before: those of the tree (named or literal) and those declared by imported
// packages. A renamed method could otherwise make a type stop, or start, satisfying one, which
// changes behaviour wherever an interface conversion or type assertion is dynamic.
func (c *comparer) compareMethodSets() {
	ifaces := c.ifaces
	std, labels := stdInterfaces(c.old)
	for i, it := range std {
		ifaces = append(ifaces, ifacePair{it, it, labels[i]})
	}
	type seenKey struct{ o, n *types.Interface }
	seen := map[seenKey]bool{}
	var unique []ifacePair
	for _, ip := range ifaces {
		if !seen[seenKey{ip.old, ip.new}] {
			seen[seenKey{ip.old, ip.new}] = true
			unique = append(unique, ip)
		}
	}
	for _, tp := range c.types {
		oldTypes, newTypes := implementers(tp.old), implementers(tp.new)
		if len(oldTypes) != len(newTypes) {
			continue // an interface became a non-interface: already reported by the syntax walk
		}
		for k := range oldTypes {
			ot, nt := oldTypes[k], newTypes[k]
			oNames, nNames := methodNames(ot), methodNames(nt)
			for _, ip := range unique {
				oImpl := hasAllMethods(oNames, ip.old) && types.Implements(ot, ip.old)
				nImpl := hasAllMethods(nNames, ip.new) && types.Implements(nt, ip.new)
				star := ""
				if k == 1 {
					star = "*"
				}
				if oImpl != nImpl {
					c.violation(tp.new.Obj().Pos(), tp.old.Obj().Pos(), "method set changed: %s%s implements %s: was %v, now %v",
						star, tp.label, ip.label, oImpl, nImpl)
				} else if oImpl {
					c.sameImplementation(ot, nt, ip, star+tp.label, tp)
				}
			}
		}
	}
}

// sameImplementation checks, for a type that implements an interface in both trees, that each
// interface method is implemented by corresponding methods. Swapping two methods' names by hand
// keeps every Implements answer but changes which body a dynamic call runs.
func (c *comparer) sameImplementation(ot, nt types.Type, ip ifacePair, label string, tp typePair) {
	for i := 0; i < ip.old.NumMethods(); i++ {
		om := ip.old.Method(i)
		nm := c.counterpartMethod(om, ip.new)
		if nm == nil {
			c.violation(tp.new.Obj().Pos(), tp.old.Obj().Pos(), "%s: method %s has no counterpart in the new interface", ip.label, om.Name())
			continue
		}
		oImpl, _, _ := types.LookupFieldOrMethod(ot, true, om.Pkg(), om.Name())
		nImpl, _, _ := types.LookupFieldOrMethod(nt, true, nm.Pkg(), nm.Name())
		if !c.corresponds(oImpl, nImpl) {
			c.violation(tp.new.Obj().Pos(), tp.old.Obj().Pos(), "%s implements %s.%s with a different method: %s, was %s",
				label, ip.label, om.Name(), c.describeObject(c.new, nImpl), c.describeObject(c.old, oImpl))
		}
	}
}

// counterpartMethod finds the method of the new interface that corresponds to om.
func (c *comparer) counterpartMethod(om *types.Func, newIface *types.Interface) *types.Func {
	key := c.old.keyOf(om)
	for j := 0; j < newIface.NumMethods(); j++ {
		nm := newIface.Method(j)
		if key == "" && origin(nm) == origin(om) {
			return nm
		}
		if p := c.res.Pairs[key]; key != "" && p != nil && p.NewKey == c.new.keyOf(nm) {
			return nm
		}
	}
	return nil
}

// corresponds reports whether an old and a new object are the same object up to renaming.
func (c *comparer) corresponds(o, n types.Object) bool {
	if o == nil || n == nil {
		return o == nil && n == nil
	}
	ok, nk := c.old.keyOf(o), c.new.keyOf(n)
	if ok != "" || nk != "" {
		p := c.res.Pairs[ok]
		return p != nil && p.NewKey == nk
	}
	return sameExternal(o, n)
}

func (c *comparer) describeObject(t *Tree, obj types.Object) string {
	if obj == nil {
		return "nothing"
	}
	if key := t.keyOf(obj); key != "" {
		return fmt.Sprintf("%s declared at %s", obj.Name(), key)
	}
	return obj.String()
}

// nameLookupGuards fails the check, when a field or method was renamed, if the old tree looks
// names up at run time in ways the other checks cannot see: reflection by name, templates parsed
// from Go strings or from files read at run time (only templates/ files are checked), and .go files
// that the build excludes on this machine but that mention a renamed name.
func (c *comparer) nameLookupGuards() {
	renamed := map[string]bool{}
	renamedMember := false
	for _, p := range c.res.Pairs {
		if p.OldName != p.NewName {
			renamed[p.OldName] = true
			if p.Kind == "struct field" || p.Kind == "method" {
				renamedMember = true
			}
		}
	}
	if renamedMember {
		lookups := map[string]bool{"reflect.FieldByName": true, "reflect.FieldByNameFunc": true, "reflect.MethodByName": true,
			"text/template.Parse": true, "text/template.ParseFiles": true, "text/template.ParseGlob": true,
			"html/template.Parse": true, "html/template.ParseFiles": true, "html/template.ParseGlob": true}
		for _, v := range c.old.Variants {
			for id, obj := range v.Info.Uses {
				if f, ok := obj.(*types.Func); ok && f.Pkg() != nil && lookups[f.Pkg().Path()+"."+f.Name()] {
					c.report(fmt.Sprintf("%s: %s looks names up at run time; renamed fields and methods cannot be proven unaffected",
						c.old.keyAt(id.Pos()), f.FullName()))
				}
			}
		}
	}
	for _, rel := range c.old.Excluded {
		src, err := c.old.read(rel)
		if err != nil {
			c.report(rel + ": " + err.Error())
			continue
		}
		fset := token.NewFileSet()
		var s scanner.Scanner
		s.Init(fset.AddFile(rel, -1, len(src)), src, nil, 0)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.IDENT && renamed[lit] {
				c.report(fmt.Sprintf("%s:%d: this file is excluded by build constraints here, so it was not renamed or checked, "+
					"but it mentions the renamed name %s", rel, fset.Position(pos).Line, lit))
			}
		}
	}
}

// printRenameSummary prints how many objects were renamed, by kind.
func printRenameSummary(w *os.File, res *Result) {
	byKind := map[string]int{}
	renamed := 0
	for _, p := range res.Pairs {
		if p.OldName != p.NewName {
			byKind[p.Kind]++
			renamed++
		}
	}
	fmt.Fprintf(w, "objects: %d, renamed: %d\n", len(res.Pairs), renamed)
	for _, k := range kindOrder {
		if byKind[k] > 0 {
			fmt.Fprintf(w, "  %-14s %d\n", k, byKind[k])
		}
	}
}

// renameTable renders a markdown table of the renamed objects that are not local to a function:
// package-level objects, fields and methods, sorted by package.
func renameTable(old, new *Tree, res *Result) string {
	type row struct{ pkg, kind, oldName, newName string }
	var rows []row
	for _, p := range res.Pairs {
		if p.OldName == p.NewName || isLocal(p.oldObj) || p.newObj == nil {
			continue
		}
		rows = append(rows, row{p.oldObj.Pkg().Path(), p.Kind,
			old.qualifiedName(p.oldObj, p.OldKey), new.qualifiedName(p.newObj, p.NewKey)})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].pkg != rows[j].pkg {
			return rows[i].pkg < rows[j].pkg
		}
		return rows[i].oldName < rows[j].oldName
	})
	var b strings.Builder
	b.WriteString("| Package | Kind | Old | New |\n|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | `%s` | `%s` |\n", r.pkg, r.kind, r.oldName, r.newName)
	}
	return b.String()
}
