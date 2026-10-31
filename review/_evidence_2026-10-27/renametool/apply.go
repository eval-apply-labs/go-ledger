package main

// apply rewrites a tree according to a rename map, in four steps:
//
//  1. Validate every entry and the map as a whole (interface groups); refuse on any problem.
//  2. Rewrite in memory every identifier that declares or uses a renamed object, in every
//     variant; add json tags that keep renamed exported fields' JSON keys; gofmt changed files.
//  3. Verify: load the rewritten tree from memory and run check's semantic comparison against
//     the original. Refuse if anything differs (this is what catches capture and shadowing).
//  4. Write the changed files, and report comment lines that still mention renamed names.
//
// Nothing is written unless steps 1-3 succeed.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type renameMap struct {
	Renames []renameEntry `json:"renames"`
}

type renameEntry struct {
	Key string `json:"key"`
	Old string `json:"old"`
	New string `json:"new"`
}

func runApply(args []string) int {
	flags := flag.NewFlagSet("apply", flag.ExitOnError)
	commentsPath := flags.String("comments", "", "stale-comment report file (default: <renames.json minus .json>.stale-comments.txt)")
	flags.Parse(args)
	if flags.NArg() != 2 {
		usage()
	}
	root, mapPath := flags.Arg(0), flags.Arg(1)
	if *commentsPath == "" {
		*commentsPath = strings.TrimSuffix(mapPath, ".json") + ".stale-comments.txt"
	}
	data, err := os.ReadFile(mapPath)
	if err != nil {
		fatalf("%v", err)
	}
	var renames renameMap
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&renames); err != nil {
		fatalf("%s: %v", mapPath, err)
	}

	fset := token.NewFileSet()
	std := newStdImporter(fset)
	old, err := loadTree("old", root, fset, std, nil)
	if err != nil {
		fatalf("%v", err)
	}
	if len(old.Errors) > 0 {
		for _, e := range old.Errors {
			fmt.Fprintln(os.Stderr, "type error:", e)
		}
		fatalf("the tree does not type-check; nothing changed")
	}
	inv, err := buildInventory(old)
	if err != nil {
		fatalf("%v", err)
	}

	// 1. Validate.
	plan, problems := planRenames(old, inv, renames.Renames)
	if len(problems) == 0 {
		// 2. Rewrite in memory.
		var sources map[string][]byte
		var stats rewriteStats
		sources, stats, problems = rewrite(old, plan)
		if len(problems) == 0 {
			// 3. Verify.
			newTree, err := loadTree("new", root, fset, std, sources)
			if err != nil {
				problems = append(problems, "the rewritten tree does not load: "+err.Error())
			} else {
				res := compareTrees(old, newTree)
				for _, v := range res.Violations {
					problems = append(problems, "verification: "+v)
				}
				if len(problems) == 0 {
					// 4. Write.
					return writeResult(old, newTree, inv, plan, res, sources, stats, *commentsPath)
				}
			}
		}
	}
	for _, p := range problems {
		fmt.Println("REFUSED", p)
	}
	fmt.Printf("apply refused: %d problems; nothing changed\n", len(problems))
	return 1
}

// plan is a validated rename map.
type plan struct {
	newName  map[string]string // key -> new name: the map's entries plus implied embedded fields
	explicit int               // entries that rename something
	implied  []string          // keys of embedded fields renamed because their type is
	tags     map[string]string // field key -> json tag value that keeps the field's JSON key
}

// planRenames validates the map. Every problem is reported, not just the first.
func planRenames(t *Tree, inv *Inventory, entries []renameEntry) (*plan, []string) {
	p := &plan{newName: map[string]string{}, tags: map[string]string{}}
	var problems []string
	problem := func(e renameEntry, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("%s (%s -> %s): %s", e.Key, e.Old, e.New, fmt.Sprintf(format, args...)))
	}
	listed := map[string]bool{}
	for _, e := range entries {
		if listed[e.Key] {
			problem(e, "the key is listed more than once")
			continue
		}
		listed[e.Key] = true
		r := inv.byKey[e.Key]
		if r == nil {
			problem(e, "no object of the tree is declared at this key")
			continue
		}
		bad := false
		check := func(failed bool, format string, args ...any) {
			if failed {
				problem(e, format, args...)
				bad = true
			}
		}
		check(r.Name != e.Old, "the object declared here is named %q", r.Name)
		check(!token.IsIdentifier(e.New), "%q is not a valid identifier (or is a keyword)", e.New)
		check(e.New == "_", "the new name is the blank identifier")
		check(types.Universe.Lookup(e.New) != nil, "%q is a predeclared identifier", e.New)
		check(ast.IsExported(e.Old) != ast.IsExported(e.New) && !localKinds[r.Kind] && r.Enclosing == "", "exportedness would change")
		check(r.Fixed, "fixed: %s", r.FixedReason)
		check(r.Kind == "package func" && r.TestFile && testKind(e.Old) != testKind(e.New),
			"a Test/Benchmark/Example/Fuzz function must keep its prefix")
		if bad || e.Old == e.New {
			continue
		}
		p.newName[e.Key] = e.New
		p.explicit++
	}

	// An embedded field is named after its type: renaming the type renames the field (and the
	// selectors that use it).
	for _, typeKey := range sortedKeys(p.newName) {
		for _, fieldKey := range inv.embeddedBy[typeKey] {
			field := inv.byKey[fieldKey]
			e := renameEntry{fieldKey, field.Name, p.newName[typeKey]}
			if listed[fieldKey] {
				problem(e, "embedded field: rename its type (%s) instead", typeKey)
				continue
			}
			if where, ok := inv.TemplateNames[field.Name]; ok && field.Exported {
				problem(e, "renaming type %s renames this embedded field, which the templates use as .%s (%s)",
					typeKey, field.Name, where)
				continue
			}
			p.newName[fieldKey] = p.newName[typeKey]
			p.implied = append(p.implied, fieldKey)
		}
	}

	// Interface groups: all members get the same new name, or none is renamed.
	done := map[string]bool{}
	for _, key := range sortedKeys(p.newName) {
		members := inv.groups.members(key)
		if len(members) < 2 || done[inv.groups.find(key)] {
			continue
		}
		done[inv.groups.find(key)] = true
		consistent := true
		for _, m := range members {
			if isExternalMember(m) || p.newName[m] != p.newName[key] {
				consistent = false
			}
		}
		if !consistent {
			var lines []string
			for _, m := range members {
				given := p.newName[m]
				if given == "" {
					given = "(not renamed)"
				}
				lines = append(lines, fmt.Sprintf("    %s %s -> %s", m, inv.groups.names[m], given))
			}
			problems = append(problems, fmt.Sprintf("%s: interface group renamed inconsistently; these methods must all get the same new name:\n%s",
				key, strings.Join(lines, "\n")))
		}
	}

	// JSON: a renamed exported field whose JSON key is its Go name gets a json tag naming the old
	// key. One tag serves every name of a declaration like `A, B int`, so such a field cannot be
	// handled automatically.
	for _, key := range sortedKeys(p.newName) {
		r := inv.byKey[key]
		field, ok := inv.objOf[key].(*types.Var)
		if !ok || !field.IsField() || !r.Exported {
			continue
		}
		decl := t.fieldDecls()[key]
		if decl == nil {
			problems = append(problems, key+": internal error: no declaration found for field")
			continue
		}
		view := jsonViewOf(field, decl.Tag)
		if view.Ignored || view.Flattened || view.HasName {
			continue
		}
		if len(decl.Field.Names) > 1 {
			problems = append(problems, fmt.Sprintf("%s (%s -> %s): the field shares its declaration (and its tag) with other "+
				"names; split the declaration first so it can get its own json tag", key, r.Name, p.newName[key]))
			continue
		}
		p.tags[key] = r.Name + view.Options
	}
	sort.Strings(problems)
	return p, problems
}

type rewriteStats struct {
	identifiers int // identifier occurrences rewritten
	files       int
	tags        int
}

// edit replaces src[off:end] with text.
type edit struct {
	off, end int
	text     string
}

// rewrite computes the new contents of every changed file.
func rewrite(t *Tree, p *plan) (map[string][]byte, rewriteStats, []string) {
	var stats rewriteStats
	var problems []string
	edits := map[*File]map[int]edit{}
	addEdit := func(f *File, e edit) {
		if edits[f] == nil {
			edits[f] = map[int]edit{}
		}
		if prev, ok := edits[f][e.off]; ok && prev != e {
			problems = append(problems, fmt.Sprintf("%s: internal error: conflicting edits at offset %d", f.Rel, e.off))
		}
		edits[f][e.off] = e
	}

	// Identifiers: every identifier, in every variant, that declares or uses a renamed object.
	// The same identifier seen in two variants yields the same edit, kept once.
	for _, v := range t.Variants {
		for _, f := range v.Files {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				def, use := t.resolve(v, f, id)
				newName := ""
				for _, k := range []string{def.key, use.key} {
					if nn, ok := p.newName[k]; ok && k != "" {
						if newName != "" && newName != nn {
							problems = append(problems, fmt.Sprintf("%s: internal error: identifier %s would get two names (%s, %s)",
								t.keyAt(id.Pos()), id.Name, newName, nn))
						}
						newName = nn
					}
				}
				if newName != "" {
					off := f.Tok.Offset(id.Pos())
					addEdit(f, edit{off, off + len(id.Name), newName})
				}
				return true
			})
		}
	}
	for _, f := range t.Files {
		stats.identifiers += len(edits[f])
	}

	// Tags.
	for _, key := range sortedKeys(p.tags) {
		decl := t.fieldDecls()[key]
		addEdit(decl.File, tagEdit(decl.File, decl.Field, p.tags[key]))
		stats.tags++
	}

	sources := map[string][]byte{}
	for _, f := range t.Files {
		if len(edits[f]) == 0 {
			continue
		}
		src, err := applyEdits(f, edits[f])
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		formatted, err := format.Source(src)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: rewritten file does not format: %v", f.Rel, err))
			continue
		}
		sources[f.Rel] = formatted
		stats.files++
	}
	return sources, stats, problems
}

// tagEdit sets the json tag value of a field declaration: it rewrites the existing tag literal
// (keeping every other key and the literal's quoting) or adds a tag after the field's type.
func tagEdit(f *File, field *ast.Field, jsonValue string) edit {
	if field.Tag == nil {
		off := f.Tok.Offset(field.Type.End())
		return edit{off, off, " `json:" + strconv.Quote(jsonValue) + "`"}
	}
	tag, _ := strconv.Unquote(field.Tag.Value)
	tag = setTagValue(tag, "json", jsonValue)
	literal := "`" + tag + "`"
	if strings.HasPrefix(field.Tag.Value, `"`) || strings.Contains(tag, "`") {
		literal = strconv.Quote(tag)
	}
	off := f.Tok.Offset(field.Tag.Pos())
	return edit{off, off + len(field.Tag.Value), literal}
}

// applyEdits applies non-overlapping edits to a file's source, checking that each renamed
// identifier still has its old text.
func applyEdits(f *File, byOffset map[int]edit) ([]byte, error) {
	edits := make([]edit, 0, len(byOffset))
	for _, e := range byOffset {
		edits = append(edits, e)
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].off != edits[j].off {
			return edits[i].off < edits[j].off
		}
		return edits[i].end < edits[j].end
	})
	var out bytes.Buffer
	prev := 0
	for _, e := range edits {
		if e.off < prev || e.end > len(f.Src) {
			return nil, fmt.Errorf("%s: internal error: overlapping edits at offset %d", f.Rel, e.off)
		}
		out.Write(f.Src[prev:e.off])
		out.WriteString(e.text)
		prev = e.end
	}
	out.Write(f.Src[prev:])
	return out.Bytes(), nil
}

// writeResult writes the verified files and the stale-comment report, and prints a summary.
func writeResult(old, new *Tree, inv *Inventory, p *plan, res *Result, sources map[string][]byte,
	stats rewriteStats, commentsPath string) int {
	for _, rel := range sortedKeys(sources) {
		info, err := os.Stat(old.abs(rel))
		if err != nil {
			fatalf("%v", err)
		}
		if err := os.WriteFile(old.abs(rel), sources[rel], info.Mode().Perm()); err != nil {
			fatalf("writing %s: %v (the tree is now partly rewritten; start again from a fresh copy)", rel, err)
		}
	}
	report, err := staleComments(old, new, inv, p, res)
	if err != nil {
		fatalf("stale-comment report: %v", err)
	}
	if err := os.WriteFile(commentsPath, []byte(strings.Join(report, "\n")+"\n"), 0o644); err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("apply: renamed %d objects (+%d embedded fields named after a renamed type)\n", p.explicit, len(p.implied))
	fmt.Printf("  rewrote %d identifier occurrences in %d files, added or extended %d json tags\n", stats.identifiers, stats.files, stats.tags)
	fmt.Printf("  verified: the rewritten tree type-checks and is the same program (%d files, %d objects compared)\n",
		res.Files, len(res.Pairs))
	fmt.Printf("  stale comment lines: %d, listed in %s\n", len(report), commentsPath)
	for _, w := range res.Warnings {
		fmt.Println("  WARNING", w)
	}
	for _, rel := range new.Excluded {
		fmt.Printf("  WARNING: %s is excluded by build constraints on this machine and was not renamed\n", rel)
	}
	return 0
}

// staleComments lists comment lines (and Markdown lines) that mention a renamed object's old name
// as a whole word: for function-local objects, only within the enclosing declaration (with its doc
// comment); for package-level objects, fields and methods, in every .go file of the tree and every
// Markdown file outside review/ except findings.md. Line numbers refer to the rewritten files.
func staleComments(old, new *Tree, inv *Inventory, p *plan, res *Result) ([]string, error) {
	type note struct{ oldName, newName string }
	type span struct {
		first, last int
		note        note
	}
	global := map[string][]note{} // old name -> renames
	local := map[string][]span{}  // new file -> local renames with their line spans
	for key, newName := range p.newName {
		r := inv.byKey[key]
		n := note{r.Name, newName}
		if !isLocal(inv.objOf[key]) {
			if !containsNote(global[r.Name], n) {
				global[r.Name] = append(global[r.Name], n)
			}
			continue
		}
		pair := res.Pairs[key]
		if pair == nil {
			continue
		}
		pos := new.posOfKey(pair.NewKey)
		f := new.fileAt(pos)
		if f == nil {
			continue
		}
		first, last := new.declLines(f, pos)
		local[f.Rel] = append(local[f.Rel], span{first, last, n})
	}

	var report []string
	seen := map[string]bool{}
	match := func(rel string, line int, text string, isComment bool) {
		for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) }) {
			var hits []note
			hits = append(hits, global[w]...)
			if isComment {
				for _, s := range local[rel] {
					if s.note.oldName == w && line >= s.first && line <= s.last && !containsNote(hits, s.note) {
						hits = append(hits, s.note)
					}
				}
			}
			for _, h := range hits {
				entry := fmt.Sprintf("%s:%d: %s -> %s: %s", rel, line, h.oldName, h.newName, strings.TrimSpace(text))
				if !seen[entry] {
					seen[entry] = true
					report = append(report, entry)
				}
			}
		}
	}

	// Comments of the rewritten .go files, and of .go files the build leaves out.
	goFiles := map[string]*ast.File{}
	for _, f := range new.Files {
		goFiles[f.Rel] = f.AST
	}
	for _, rel := range new.Excluded {
		src, err := new.read(rel)
		if err != nil {
			return nil, err
		}
		af, err := parser.ParseFile(new.Fset, rel, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		goFiles[rel] = af
	}
	for _, rel := range sortedKeys(goFiles) {
		for _, group := range goFiles[rel].Comments {
			for _, c := range group.List {
				first := new.Fset.Position(c.Pos()).Line
				for i, text := range strings.Split(c.Text, "\n") {
					match(rel, first+i, text, true)
				}
			}
		}
	}

	// Markdown files.
	err := filepath.WalkDir(new.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := new.rel(p)
		if d.IsDir() {
			if rel == "review" || rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if path.Ext(rel) != ".md" || path.Base(rel) == "findings.md" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, text := range strings.Split(string(data), "\n") {
			match(rel, i+1, text, false)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(report, func(i, j int) bool { return lessLocation(report[i], report[j]) })
	return report, nil
}

func containsNote[T comparable](list []T, x T) bool {
	for _, y := range list {
		if y == x {
			return true
		}
	}
	return false
}

// lessLocation orders "file:line: ..." strings by file, then numerically by line.
func lessLocation(a, b string) bool {
	fa, la := splitLocation(a)
	fb, lb := splitLocation(b)
	if fa != fb {
		return fa < fb
	}
	return la < lb
}

func splitLocation(s string) (string, int) {
	file, rest, _ := strings.Cut(s, ":")
	lineText, _, _ := strings.Cut(rest, ":")
	line, _ := strconv.Atoi(lineText)
	return file, line
}
