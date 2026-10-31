package main

// Interface groups. When a module type implements a module interface, the interface's methods and
// the type's methods of the same names must keep matching names, or the type stops implementing
// the interface. A group is the closure of "is the same method slot as" over every (named type,
// named interface) pair of the module for which types.Implements holds; every member of a group
// must be renamed together, to the same new name.

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
)

// Groups is a union-find over method keys. Methods declared outside the tree take part as
// "external pkg.Type.Method" members; a group with one can never be renamed.
type Groups struct {
	parent map[string]string
	names  map[string]string // member key -> qualified name, for messages
}

func (g *Groups) add(key, name string) {
	if _, ok := g.parent[key]; !ok {
		g.parent[key] = key
		g.names[key] = name
	}
}

func (g *Groups) find(key string) string {
	for g.parent[key] != key {
		g.parent[key] = g.parent[g.parent[key]] // path halving
		key = g.parent[key]
	}
	return key
}

func (g *Groups) union(a, b string) { g.parent[g.find(a)] = g.find(b) }

// members returns the sorted members of key's group, or nil when key is in no group.
func (g *Groups) members(key string) []string {
	if _, ok := g.parent[key]; !ok {
		return nil
	}
	root := g.find(key)
	var out []string
	for k := range g.parent {
		if g.find(k) == root {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func isExternalMember(key string) bool { return strings.HasPrefix(key, "external ") }

// computeGroups builds the groups from every variant of the tree. Named types and interfaces of
// all variants take part (a test variant's types are distinct objects from the base variant's,
// with the same keys), which can only add links, never lose one.
func computeGroups(t *Tree) *Groups {
	g := &Groups{parent: map[string]string{}, names: map[string]string{}}
	memberKey := func(f *types.Func) string {
		if k := t.keyOf(f); k != "" {
			g.add(k, t.qualifiedName(f, k))
			return k
		}
		k := "external " + f.FullName()
		g.add(k, f.FullName())
		return k
	}

	var named []*types.Named  // every non-generic named type declared in the tree
	var ifaces []*types.Named // those whose underlying type is an interface with methods
	for _, v := range t.Variants {
		for _, key := range sortedKeys(t.defsByKey(v)) {
			tn, ok := t.defsByKey(v)[key].(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			nt, ok := tn.Type().(*types.Named)
			if !ok || nt.TypeParams().Len() > 0 {
				continue
			}
			named = append(named, nt)
			if it, ok := nt.Underlying().(*types.Interface); ok && it.NumMethods() > 0 {
				ifaces = append(ifaces, nt)
			}
		}
	}
	for _, in := range ifaces {
		it := in.Underlying().(*types.Interface)
		for i := 0; i < it.NumMethods(); i++ {
			memberKey(it.Method(i))
		}
	}
	for _, nt := range named {
		for _, typ := range implementers(nt) {
			names := methodNames(typ)
			for _, in := range ifaces {
				it := in.Underlying().(*types.Interface)
				if in == nt || !hasAllMethods(names, it) || !types.Implements(typ, it) {
					continue
				}
				for i := 0; i < it.NumMethods(); i++ {
					m := it.Method(i)
					obj, _, _ := types.LookupFieldOrMethod(typ, true, m.Pkg(), m.Name())
					if impl, ok := obj.(*types.Func); ok {
						g.union(memberKey(m), memberKey(impl))
					}
				}
			}
		}
	}
	return g
}

// implementers returns the types whose method sets a named type contributes: T, and *T unless T
// is an interface (a pointer to an interface has no methods).
func implementers(nt *types.Named) []types.Type {
	if types.IsInterface(nt) {
		return []types.Type{nt}
	}
	return []types.Type{nt, types.NewPointer(nt)}
}

// methodNames returns the names in typ's method set, a cheap pre-filter for types.Implements.
func methodNames(typ types.Type) map[string]bool {
	ms := types.NewMethodSet(typ)
	names := make(map[string]bool, ms.Len())
	for i := 0; i < ms.Len(); i++ {
		names[ms.At(i).Obj().Name()] = true
	}
	return names
}

func hasAllMethods(names map[string]bool, it *types.Interface) bool {
	for i := 0; i < it.NumMethods(); i++ {
		if !names[it.Method(i).Name()] {
			return false
		}
	}
	return true
}

// externalPackages returns every package outside the tree that the tree imports, directly or
// transitively, sorted by path.
func externalPackages(t *Tree) []*types.Package {
	seen := map[*types.Package]bool{}
	var out []*types.Package
	var visit func(p *types.Package)
	visit = func(p *types.Package) {
		if p == nil || seen[p] {
			return
		}
		seen[p] = true
		if t.byPath[p.Path()] == nil && !strings.HasSuffix(p.Path(), "_test") {
			out = append(out, p)
		}
		for _, q := range p.Imports() {
			visit(q)
		}
	}
	for _, v := range t.Variants {
		visit(v.Types)
	}
	// Public packages first, so that messages name an interface a reader can look up.
	internal := func(p *types.Package) bool {
		return strings.Contains(p.Path(), "internal") || strings.HasPrefix(p.Path(), "vendor/")
	}
	sort.Slice(out, func(i, j int) bool {
		if internal(out[i]) != internal(out[j]) {
			return !internal(out[i])
		}
		return out[i].Path() < out[j].Path()
	})
	return out
}

// externalInterfaceMethods maps each exported method name of every interface declared in the
// scope of an external package the tree imports (transitively), plus the predeclared error, to
// the first such interface. A module method with one of these names may satisfy that interface
// for the standard library, so it keeps its name. (Unexported method names cannot be satisfied
// from another package and are left out.)
func externalInterfaceMethods(t *Tree) map[string]string {
	names := map[string]string{"Error": "error (predeclared)"}
	for _, p := range externalPackages(t) {
		scope := p.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			it, ok := tn.Type().Underlying().(*types.Interface)
			if !ok {
				continue
			}
			for i := 0; i < it.NumMethods(); i++ {
				m := it.Method(i).Name()
				if _, ok := names[m]; !ok && ast.IsExported(m) {
					names[m] = p.Path() + "." + name
				}
			}
		}
	}
	return names
}

// anonymousInterfaceMethods maps each method name of every interface literal in module code that
// is not itself a type declaration (e.g. x.(interface{ Close() error })) to its position. Such an
// interface can match any type by method name at run time.
func anonymousInterfaceMethods(t *Tree) map[string]string {
	names := map[string]string{}
	for _, v := range t.Variants {
		for _, f := range v.Files {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.InterfaceType)
				if !ok {
					return true
				}
				if _, named := f.NamedIfaces[lit]; named {
					return true
				}
				it, ok := v.Info.TypeOf(lit).(*types.Interface)
				if !ok {
					return true
				}
				for i := 0; i < it.NumMethods(); i++ {
					if _, seen := names[it.Method(i).Name()]; !seen {
						names[it.Method(i).Name()] = t.keyAt(lit.Pos())
					}
				}
				return true
			})
		}
	}
	return names
}

// stdInterfaces returns every non-generic interface declared in the scope of an external package
// the tree imports, plus the predeclared error, with display names.
func stdInterfaces(t *Tree) ([]*types.Interface, []string) {
	var ifaces []*types.Interface
	var labels []string
	ifaces = append(ifaces, types.Universe.Lookup("error").Type().Underlying().(*types.Interface))
	labels = append(labels, "error")
	for _, p := range externalPackages(t) {
		scope := p.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			nt, ok := tn.Type().(*types.Named)
			if !ok || nt.TypeParams().Len() > 0 {
				continue
			}
			if it, ok := nt.Underlying().(*types.Interface); ok && it.NumMethods() > 0 {
				ifaces = append(ifaces, it)
				labels = append(labels, p.Path()+"."+name)
			}
		}
	}
	return ifaces, labels
}
