package main

// Objects and their keys. Every object declared in the tree is identified by the position of its
// declaring identifier, "file:line:col" with the file relative to the tree root. The base and test
// variants of a package type-check the same parsed files, so their objects for one declaration
// share a key, while their *types.Object pointers differ.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

// fileAt returns the tree file containing pos, or nil when pos is elsewhere (GOROOT, another tree).
func (t *Tree) fileAt(pos token.Pos) *File {
	if !pos.IsValid() {
		return nil
	}
	return t.byToken[t.Fset.File(pos)]
}

// keyAt returns the key of a position in the tree, or "" when the position is not in the tree.
func (t *Tree) keyAt(pos token.Pos) string {
	f := t.fileAt(pos)
	if f == nil {
		return ""
	}
	p := t.Fset.Position(pos)
	return fmt.Sprintf("%s:%d:%d", f.Rel, p.Line, p.Column)
}

// keyOf returns the key of an object declared in the tree, or "" for objects declared elsewhere
// (standard library, universe) and for package names (imports), which the tool never renames.
func (t *Tree) keyOf(obj types.Object) string {
	if obj == nil {
		return ""
	}
	if _, ok := obj.(*types.PkgName); ok {
		return ""
	}
	return t.keyAt(obj.Pos())
}

// posOfKey turns a key back into a position (token.NoPos when it does not name a tree position).
func (t *Tree) posOfKey(key string) token.Pos {
	rest, colText, ok1 := cutLast(key, ":")
	rel, lineText, ok2 := cutLast(rest, ":")
	f := t.ByRel[rel]
	line, err1 := strconv.Atoi(lineText)
	col, err2 := strconv.Atoi(colText)
	if !ok1 || !ok2 || f == nil || err1 != nil || err2 != nil || line < 1 || line > f.Tok.LineCount() {
		return token.NoPos
	}
	return f.Tok.LineStart(line) + token.Pos(col-1)
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// position formats any position, in the tree or not, for messages.
func (t *Tree) position(pos token.Pos) string {
	if key := t.keyAt(pos); key != "" {
		return key
	}
	if !pos.IsValid() {
		return "-"
	}
	return t.Fset.Position(pos).String()
}

// ref is what an identifier stands for in one variant: an object declared in the tree (key set),
// an object declared elsewhere (obj set, key empty), or nothing (both empty).
type ref struct {
	obj types.Object // nil for a type-switch guard
	key string
}

func (r ref) none() bool { return r.obj == nil && r.key == "" }

// resolve returns what identifier id declares (def) and what it uses (use) in variant v.
//
// Usually at most one is set. An embedded field's identifier has both: it declares the field and
// uses the embedded type. A type-switch guard (x in `switch x := y.(type)`) declares nothing in
// go/types; each case clause declares its own implicit x at the guard's position instead, so the
// guard's own key stands for all of them.
func (t *Tree) resolve(v *Variant, f *File, id *ast.Ident) (def, use ref) {
	if obj := v.Info.Defs[id]; obj != nil {
		def = ref{obj, t.keyOf(obj)}
	} else if f.Guards[id] != nil {
		def = ref{nil, t.keyAt(id.Pos())}
	}
	if obj := v.Info.Uses[id]; obj != nil {
		use = ref{obj, t.keyOf(obj)}
	}
	return def, use
}

// kindOf names an object's kind as the inventory reports it. A nil object is a type-switch guard.
// localKinds are the kinds of objects declared inside a function. Go exports only package-level
// names, fields and methods, so the case of a local name's first letter has no meaning and may
// change (a test's local key string A may become founderKey).
var localKinds = map[string]bool{"local var": true, "param": true, "result": true, "receiver": true}

// isFunctionLocal reports whether obj (a constant or type, typically) is declared inside a function,
// where the case of its first letter has no meaning either.
func isFunctionLocal(obj types.Object) bool {
	return obj != nil && obj.Pkg() != nil && obj.Parent() != nil && obj.Parent() != obj.Pkg().Scope()
}

func kindOf(obj types.Object) string {
	switch obj := obj.(type) {
	case nil:
		return "local var"
	case *types.Func:
		if obj.Signature().Recv() != nil {
			return "method"
		}
		return "package func"
	case *types.TypeName:
		if _, ok := obj.Type().(*types.TypeParam); ok {
			return "type param"
		}
		return "type"
	case *types.Const:
		return "const"
	case *types.Var:
		switch obj.Kind() {
		case types.PackageVar:
			return "var"
		case types.LocalVar:
			return "local var"
		case types.RecvVar:
			return "receiver"
		case types.ParamVar:
			return "param"
		case types.ResultVar:
			return "result"
		case types.FieldVar:
			return "struct field"
		}
	case *types.Label:
		return "label"
	case *types.PkgName:
		return "package name"
	}
	return "other"
}

// isLocal reports whether an object is declared inside a function or a type/func signature, as
// opposed to package level, struct fields and methods. A nil object (type-switch guard) is local.
func isLocal(obj types.Object) bool {
	switch o := obj.(type) {
	case nil, *types.Label:
		return true
	case *types.Func:
		return false
	case *types.Var:
		if o.IsField() {
			return false
		}
	}
	return obj.Pkg() != nil && obj.Parent() != obj.Pkg().Scope()
}

// typeString describes an object's type relative to its package. For a declared type it gives
// the underlying type (abbreviated for structs and interfaces), not the type's own name.
func typeString(obj types.Object) string {
	q := qualifier(obj.Pkg())
	switch obj := obj.(type) {
	case *types.Label:
		return ""
	case *types.TypeName:
		if obj.IsAlias() {
			return "= " + types.TypeString(types.Unalias(obj.Type()), q)
		}
		if tp, ok := obj.Type().(*types.TypeParam); ok {
			return types.TypeString(tp.Constraint(), q)
		}
		switch u := obj.Type().Underlying().(type) {
		case *types.Struct:
			return fmt.Sprintf("struct (%d fields)", u.NumFields())
		case *types.Interface:
			return fmt.Sprintf("interface (%d methods)", u.NumMethods())
		default:
			return types.TypeString(u, q)
		}
	}
	return types.TypeString(obj.Type(), q)
}

// enclosingName names the top-level declaration containing pos: "Func" or "Type.Method" for a
// function, otherwise the declared type/var/const name, with ".Member" when pos is inside an
// interface method or struct field of a type declaration.
func enclosingName(f *File, pos token.Pos) string {
	for _, d := range f.AST.Decls {
		if pos < d.Pos() || pos >= d.End() {
			continue
		}
		switch d := d.(type) {
		case *ast.FuncDecl:
			return funcDeclName(d)
		case *ast.GenDecl:
			for _, s := range d.Specs {
				if pos < s.Pos() || pos >= s.End() {
					continue
				}
				switch s := s.(type) {
				case *ast.TypeSpec:
					return s.Name.Name + memberAt(s.Type, pos)
				case *ast.ValueSpec:
					var names []string
					for _, n := range s.Names {
						names = append(names, n.Name)
					}
					return strings.Join(names, ", ")
				}
			}
		}
	}
	return ""
}

func memberAt(typ ast.Expr, pos token.Pos) string {
	var list *ast.FieldList
	switch t := typ.(type) {
	case *ast.StructType:
		list = t.Fields
	case *ast.InterfaceType:
		list = t.Methods
	}
	if list == nil {
		return ""
	}
	for _, field := range list.List {
		if pos >= field.Pos() && pos < field.End() && len(field.Names) > 0 {
			return "." + field.Names[0].Name
		}
	}
	return ""
}

func funcDeclName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	return recvBaseName(d.Recv.List[0].Type) + "." + d.Name.Name
}

func recvBaseName(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return "?"
		}
	}
}

// declLines returns the first and last line of the top-level declaration containing pos,
// including its doc comment (where parameters are usually described).
func (t *Tree) declLines(f *File, pos token.Pos) (first, last int) {
	for _, d := range f.AST.Decls {
		if pos < d.Pos() || pos >= d.End() {
			continue
		}
		start := d.Pos()
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
		case *ast.GenDecl:
			if d.Doc != nil {
				start = d.Doc.Pos()
			}
		}
		return t.Fset.Position(start).Line, t.Fset.Position(d.End()).Line
	}
	return 0, 0
}

// embeddedIdent returns the type-name identifier of an embedded field's type (T in T, *T, p.T,
// T[int]); go/types declares the field at that identifier.
func embeddedIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.SelectorExpr:
			return x.Sel
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x
		default:
			return nil
		}
	}
}

// fieldDecl is the syntax of one struct field declaration.
type fieldDecl struct {
	Owner string     // owning named type ("Config", "Config.Inner" for a nested anonymous struct)
	Field *ast.Field // the declaration (may declare several names sharing one tag)
	Tag   string     // the unquoted tag, "" if none
	File  *File
}

// fieldDecls maps the key of every struct field declared in the tree to its declaration. Fields of
// anonymous structs that are not inside a type declaration get the owner "(anonymous struct)".
func (t *Tree) fieldDecls() map[string]*fieldDecl {
	if t.fields != nil {
		return t.fields
	}
	t.fields = map[string]*fieldDecl{}
	for _, f := range t.Files {
		visited := map[*ast.StructType]bool{}
		var walk func(st *ast.StructType, owner string)
		walk = func(st *ast.StructType, owner string) {
			visited[st] = true
			for _, field := range st.Fields.List {
				tag := ""
				if field.Tag != nil {
					tag, _ = strconv.Unquote(field.Tag.Value)
				}
				names := field.Names
				if len(names) == 0 {
					if id := embeddedIdent(field.Type); id != nil {
						names = []*ast.Ident{id}
					}
				}
				for _, id := range names {
					t.fields[t.keyAt(id.Pos())] = &fieldDecl{Owner: owner, Field: field, Tag: tag, File: f}
				}
				inner := owner
				if len(field.Names) > 0 {
					inner = owner + "." + field.Names[0].Name
				}
				ast.Inspect(field.Type, func(n ast.Node) bool {
					if s, ok := n.(*ast.StructType); ok && !visited[s] {
						walk(s, inner)
						return false
					}
					return true
				})
			}
		}
		ast.Inspect(f.AST, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.TypeSpec:
				if st, ok := n.Type.(*ast.StructType); ok && !visited[st] {
					walk(st, n.Name.Name)
				}
			case *ast.StructType:
				if !visited[n] {
					walk(n, "(anonymous struct)")
				}
			}
			return true
		})
	}
	return t.fields
}

// defsByKey indexes the objects variant v declares by key.
func (t *Tree) defsByKey(v *Variant) map[string]types.Object {
	if m := t.defIndex[v]; m != nil {
		return m
	}
	m := map[string]types.Object{}
	for _, obj := range v.Info.Defs {
		if k := t.keyOf(obj); k != "" {
			m[k] = obj
		}
	}
	t.defIndex[v] = m
	return m
}

// qualifiedName names an object for reports: "pkg.Name", "pkg.Type.Field", "pkg.Type.Method".
func (t *Tree) qualifiedName(obj types.Object, key string) string {
	name := obj.Name()
	switch o := obj.(type) {
	case *types.Func:
		if recv := o.Signature().Recv(); recv != nil {
			name = recvTypeName(recv.Type()) + "." + name
		}
	case *types.Var:
		if o.IsField() {
			if fd := t.fieldDecls()[key]; fd != nil {
				name = fd.Owner + "." + name
			}
		}
	}
	if obj.Pkg() != nil {
		name = obj.Pkg().Name() + "." + name
	}
	return name
}

func recvTypeName(typ types.Type) string {
	if p, ok := typ.(*types.Pointer); ok {
		typ = p.Elem()
	}
	switch x := types.Unalias(typ).(type) {
	case *types.Named:
		return x.Obj().Name()
	case *types.Interface:
		return "interface"
	}
	return typ.String()
}

// qualifier writes types of the given package unqualified and other packages' types qualified by
// package name (consensus.Action rather than github.com/eval-apply-labs/go-ledger/consensus.Action).
func qualifier(pkg *types.Package) types.Qualifier {
	return func(other *types.Package) string {
		if other == pkg {
			return ""
		}
		return other.Name()
	}
}

// origin maps an instantiated generic field or method to its declaration, so that objects of a
// generic standard-library type compare equal across trees whatever the type arguments.
func origin(obj types.Object) types.Object {
	switch o := obj.(type) {
	case *types.Var:
		return o.Origin()
	case *types.Func:
		return o.Origin()
	}
	return obj
}
