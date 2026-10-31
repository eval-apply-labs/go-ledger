package main

// The inventory: one record per object declared in the tree, with what a renamer needs to know,
// whether the object may be renamed at all (fixed), and a hint when its name looks abbreviated.

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Record describes one declared object. Key is its identity everywhere in the tool.
type Record struct {
	Key         string   `json:"key"`  // "file:line:col" of the declaring identifier
	Name        string   `json:"name"` // current name
	Kind        string   `json:"kind"` // package func, method, type, const, var, struct field, local var, param, result, receiver, label, type param
	Package     string   `json:"package"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Col         int      `json:"col"`
	Exported    bool     `json:"exported"`
	Type        string   `json:"type,omitempty"`      // relative to the package; a type's underlying type
	Enclosing   string   `json:"enclosing,omitempty"` // for function-local objects: "Func" or "Type.Method"
	Recv        string   `json:"recv,omitempty"`      // for methods: the receiver type
	Struct      string   `json:"struct,omitempty"`    // for fields: the owning named type
	Embedded    bool     `json:"embedded,omitempty"`  // for fields: an embedded field
	JSONTag     string   `json:"jsonTag,omitempty"`   // for fields: the json tag value, if any
	JSONKey     string   `json:"jsonKey,omitempty"`   // for fields: the JSON member name it produces, if any
	TestFile    bool     `json:"testFile,omitempty"`  // declared in a _test.go file
	Uses        int      `json:"uses"`                // distinct use sites across all variants
	Fixed       bool     `json:"fixed"`
	FixedReason string   `json:"fixedReason,omitempty"`
	Suggest     string   `json:"suggest,omitempty"` // why the name looks abbreviated
	Group       []string `json:"group,omitempty"`   // interface group: keys that must get the same new name
}

// Summary counts the records.
type Summary struct {
	Total              int                       `json:"total"`
	ByKind             map[string]int            `json:"byKind"`
	ByPackage          map[string]map[string]int `json:"byPackage"`
	Suggested          int                       `json:"suggested"`
	SuggestedRenamable int                       `json:"suggestedRenamable"` // suggested and not fixed
	Fixed              int                       `json:"fixed"`
	FixedBy            map[string]int            `json:"fixedByReason"` // reason category -> count
}

// Inventory is the output of `renametool inventory`, also used by apply for validation.
type Inventory struct {
	GoVersion     string            `json:"goVersion"`
	Tree          string            `json:"tree"`
	TemplateNames map[string]string `json:"templateNames"`
	Summary       *Summary          `json:"summary"`
	Objects       []*Record         `json:"objects"`

	byKey      map[string]*Record
	objOf      map[string]types.Object // a representative object per key (nil for type-switch guards)
	embeddedBy map[string][]string     // type key -> keys of the embedded fields named after it
	groups     *Groups
}

// dynamicNames are method names the standard library looks for at run time through interfaces
// that are not in any package scope (anonymous interfaces in function bodies, reflection). They are
// fixed. IsZero is added to the list given in the brief: encoding/json's omitzero option looks for
// an IsZero() bool method through an unexported interface.
var dynamicNames = setOf("String", "GoString", "Format", "Error", "Unwrap", "Is", "As", "Timeout",
	"Temporary", "MarshalJSON", "UnmarshalJSON", "MarshalText", "UnmarshalText", "MarshalBinary",
	"UnmarshalBinary", "ServeHTTP", "Len", "Less", "Swap", "Push", "Pop", "Read", "Write", "Close",
	"Seek", "ReadFrom", "WriteTo", "Flush", "Header", "WriteHeader", "Hijack", "Public", "Sign",
	"Equal", "Lock", "Unlock", "Deadline", "Done", "Err", "Value", "IsZero")

// entryPoints are function names the toolchain calls by name.
var entryPoints = setOf("main", "init", "TestMain")

func setOf(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func runInventory(args []string) int {
	flags := flag.NewFlagSet("inventory", flag.ExitOnError)
	out := flags.String("o", "", "write the JSON here instead of stdout")
	flags.Parse(args)
	if flags.NArg() != 1 {
		usage()
	}
	fset := token.NewFileSet()
	tree, err := loadTree("tree", flags.Arg(0), fset, newStdImporter(fset), nil)
	if err != nil {
		fatalf("%v", err)
	}
	if len(tree.Errors) > 0 {
		for _, e := range tree.Errors {
			fmt.Fprintln(os.Stderr, "type error:", e)
		}
		fatalf("the tree does not type-check")
	}
	inv, err := buildInventory(tree)
	if err != nil {
		fatalf("%v", err)
	}
	var w io.Writer = os.Stdout
	if *out != "" {
		file, err := os.Create(*out)
		if err != nil {
			fatalf("%v", err)
		}
		defer file.Close()
		w = file
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(inv); err != nil {
		fatalf("%v", err)
	}
	printSummary(os.Stderr, tree, inv)
	return 0
}

// buildInventory records every object the tree declares, in every variant.
func buildInventory(t *Tree) (*Inventory, error) {
	templateNames, err := templateFieldNames(t)
	if err != nil {
		return nil, err
	}
	inv := &Inventory{
		GoVersion: runtime.Version(), Tree: t.Root, TemplateNames: templateNames,
		byKey: map[string]*Record{}, objOf: map[string]types.Object{}, embeddedBy: map[string][]string{},
		groups: computeGroups(t),
	}
	external := externalInterfaceMethods(t)
	anonymous := anonymousInterfaceMethods(t)
	uses := map[string]map[token.Pos]bool{}

	for _, v := range t.Variants {
		for _, f := range v.Files {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				def, use := t.resolve(v, f, id)
				if use.key != "" {
					if uses[use.key] == nil {
						uses[use.key] = map[token.Pos]bool{}
					}
					uses[use.key][id.Pos()] = true
				}
				if def.key == "" {
					return true
				}
				if field, ok := def.obj.(*types.Var); ok && field.Embedded() && use.key != "" &&
					!containsString(inv.embeddedBy[use.key], def.key) {
					inv.embeddedBy[use.key] = append(inv.embeddedBy[use.key], def.key)
				}
				if inv.byKey[def.key] == nil {
					inv.byKey[def.key] = inv.newRecord(t, v, f, id, def.obj)
					inv.objOf[def.key] = def.obj
				}
				return true
			})
		}
	}

	for key, r := range inv.byKey {
		r.Uses = len(uses[key])
		r.Suggest = suggestFor(r.Name)
		r.FixedReason = inv.fixedReason(r, external, anonymous)
		r.Fixed = r.FixedReason != ""
		if r.Kind == "method" {
			if members := inv.groups.members(key); len(members) > 1 {
				for _, m := range members {
					if m != key {
						r.Group = append(r.Group, m)
					}
				}
			}
		}
		inv.Objects = append(inv.Objects, r)
	}
	sort.Slice(inv.Objects, func(i, j int) bool { return lessRecord(inv.Objects[i], inv.Objects[j]) })
	inv.Summary = summarize(inv.Objects)
	return inv, nil
}

func lessRecord(a, b *Record) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Col < b.Col
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// newRecord describes the object declared by identifier id (obj is nil for a type-switch guard).
func (inv *Inventory) newRecord(t *Tree, v *Variant, f *File, id *ast.Ident, obj types.Object) *Record {
	pos := t.Fset.Position(id.Pos())
	r := &Record{
		Key: t.keyAt(id.Pos()), Name: id.Name, Kind: kindOf(obj), File: f.Rel, Line: pos.Line, Col: pos.Column,
		Exported: ast.IsExported(id.Name), TestFile: f.Test,
	}
	if obj == nil { // type-switch guard
		r.Package = v.Types.Path()
		guard := f.Guards[id].Assign.(*ast.AssignStmt).Rhs[0].(*ast.TypeAssertExpr)
		r.Type = "type switch on " + types.TypeString(v.Info.TypeOf(guard.X), qualifier(v.Types))
		r.Enclosing = enclosingName(f, id.Pos())
		return r
	}
	r.Package = obj.Pkg().Path()
	r.Type = typeString(obj)
	switch o := obj.(type) {
	case *types.Func:
		if recv := o.Signature().Recv(); recv != nil {
			r.Recv = types.TypeString(recv.Type(), qualifier(o.Pkg()))
		}
	case *types.Var:
		if o.IsField() {
			r.Embedded = o.Embedded()
			if fd := t.fieldDecls()[r.Key]; fd != nil {
				r.Struct = fd.Owner
				r.JSONTag, _ = reflect.StructTag(fd.Tag).Lookup("json")
				if view := jsonViewOf(o, fd.Tag); !view.Ignored && !view.Flattened {
					r.JSONKey = view.Key
				}
			}
		}
	}
	if isLocal(obj) {
		r.Enclosing = enclosingName(f, id.Pos())
	}
	return r
}

// fixedReason says why an object must keep its name, or returns "". The text before the first
// colon is the reason's category, used for the summary.
func (inv *Inventory) fixedReason(r *Record, external, anonymous map[string]string) string {
	if r.Name == "_" {
		return "blank identifier: nothing to rename"
	}
	if r.Kind == "package func" && entryPoints[r.Name] {
		return "entry point: the toolchain calls " + r.Name + " by name"
	}
	if r.Kind == "method" {
		if dynamicNames[r.Name] {
			return "dynamic name: the standard library looks for a " + r.Name + " method at run time"
		}
		if where, ok := external[r.Name]; ok {
			return "external interface: " + r.Name + " is a method of " + where
		}
		if where, ok := anonymous[r.Name]; ok {
			return "anonymous interface: " + r.Name + " is a method of the interface literal at " + where
		}
		for _, m := range inv.groups.members(r.Key) {
			if isExternalMember(m) {
				return "external interface: in a group with " + strings.TrimPrefix(m, "external ")
			}
		}
	}
	if (r.Kind == "method" || r.Kind == "struct field") && r.Exported {
		if where, ok := inv.TemplateNames[r.Name]; ok {
			return "template: used as ." + r.Name + " in " + where
		}
	}
	if r.Kind == "struct field" && r.Embedded {
		return "embedded field: named after its type, renamed only with the type"
	}
	return ""
}

// shortOK are short names that are idiomatic and clear enough to keep.
var shortOK = setOf("err", "i", "j", "k", "t", "b", "_")

// abbreviations are camelCase pieces that usually stand for a longer word.
var abbreviations = setOf("msg", "sig", "cfg", "conf", "pk", "sk", "pub", "priv", "buf", "req", "resp",
	"res", "ctx", "idx", "cnt", "num", "prev", "cur", "curr", "val", "vals", "ev", "evt", "tx", "txs",
	"addr", "param", "params", "tmp", "tmpl", "fn", "cb", "ch", "mu", "mtx", "wg", "src", "dst", "str",
	"ret", "obj", "elem", "ptr", "ref", "rec", "recv", "hdr", "opt", "opts", "acct", "amt", "bal",
	"cert", "gov", "ver", "vs", "ms", "ts", "info", "impl", "init", "auth", "dir", "len", "cmd", "pkt",
	"sz", "ok")

// suggestFor explains why a name looks abbreviated, or returns "".
func suggestFor(name string) string {
	var reasons []string
	if utf8.RuneCountInString(name) <= 3 && !shortOK[name] {
		reasons = append(reasons, "short name")
	}
	var found []string
	for _, piece := range camelPieces(name) {
		if lower := strings.ToLower(piece); abbreviations[lower] && !containsString(found, lower) {
			found = append(found, lower)
		}
	}
	if len(found) > 0 {
		reasons = append(reasons, "abbreviation: "+strings.Join(found, ", "))
	}
	return strings.Join(reasons, "; ")
}

// camelPieces splits an identifier into its words: "networkID" -> network, ID; "HTTPServer" ->
// HTTP, Server; "sha256Sum" -> sha, 256, Sum; underscores separate words and are dropped.
func camelPieces(name string) []string {
	runes := []rune(name)
	var pieces []string
	start := 0
	flush := func(end int) {
		if end > start {
			pieces = append(pieces, string(runes[start:end]))
		}
		start = end
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '_' {
			flush(i)
			start = i + 1
			continue
		}
		if i == start {
			continue
		}
		prev := runes[i-1]
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(r):
			flush(i)
		case unicode.IsDigit(prev) != unicode.IsDigit(r):
			flush(i)
		case unicode.IsUpper(prev) && unicode.IsUpper(r) && i+1 < len(runes) && unicode.IsLower(runes[i+1]):
			flush(i)
		}
	}
	flush(len(runes))
	return pieces
}

func summarize(records []*Record) *Summary {
	s := &Summary{ByKind: map[string]int{}, ByPackage: map[string]map[string]int{}, FixedBy: map[string]int{}}
	for _, r := range records {
		s.Total++
		s.ByKind[r.Kind]++
		if s.ByPackage[r.Package] == nil {
			s.ByPackage[r.Package] = map[string]int{}
		}
		s.ByPackage[r.Package][r.Kind]++
		if r.Suggest != "" {
			s.Suggested++
			if !r.Fixed {
				s.SuggestedRenamable++
			}
		}
		if r.Fixed {
			s.Fixed++
			category, _, _ := strings.Cut(r.FixedReason, ":")
			s.FixedBy[category]++
		}
	}
	return s
}

var kindOrder = []string{"package func", "method", "type", "const", "var", "struct field", "local var",
	"param", "result", "receiver", "label", "type param"}

// printSummary prints the counts as a table: packages down, kinds across.
func printSummary(w io.Writer, t *Tree, inv *Inventory) {
	s := inv.Summary
	var kinds []string
	for _, k := range kindOrder {
		if s.ByKind[k] > 0 {
			kinds = append(kinds, k)
		}
	}
	short := func(p string) string { // trim the root module path for width
		root := t.Modules[0].Path
		if p == root {
			return "."
		}
		return strings.TrimPrefix(p, root+"/")
	}
	fmt.Fprintf(w, "%-16s", "package")
	for _, k := range kinds {
		fmt.Fprintf(w, " %8s", abbreviateKind(k))
	}
	fmt.Fprintf(w, " %8s\n", "total")
	for _, p := range sortedKeys(s.ByPackage) {
		fmt.Fprintf(w, "%-16s", short(p))
		total := 0
		for _, k := range kinds {
			fmt.Fprintf(w, " %8d", s.ByPackage[p][k])
			total += s.ByPackage[p][k]
		}
		fmt.Fprintf(w, " %8d\n", total)
	}
	fmt.Fprintf(w, "%-16s", "all")
	for _, k := range kinds {
		fmt.Fprintf(w, " %8d", s.ByKind[k])
	}
	fmt.Fprintf(w, " %8d\n", s.Total)
	fmt.Fprintf(w, "kinds: %s\n", strings.Join(kinds, ", "))
	fmt.Fprintf(w, "suggested (name looks abbreviated): %d, of which renamable: %d\n", s.Suggested, s.SuggestedRenamable)
	fmt.Fprintf(w, "fixed: %d\n", s.Fixed)
	for _, c := range sortedKeys(s.FixedBy) {
		fmt.Fprintf(w, "  %-20s %d\n", c, s.FixedBy[c])
	}
	if len(t.Excluded) > 0 {
		fmt.Fprintf(w, "files excluded by build constraints (not inventoried, not renamed): %s\n", strings.Join(t.Excluded, ", "))
	}
}

func abbreviateKind(k string) string {
	switch k {
	case "package func":
		return "func"
	case "struct field":
		return "field"
	case "local var":
		return "local"
	case "type param":
		return "typeparam"
	}
	return k
}
