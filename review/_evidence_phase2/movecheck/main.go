// movecheck compares two source trees of the go-ledger repository and reports whether every top-level
// declaration, with its doc comment and the comments inside it, is the same text in both, wherever it
// sits. Only the file may change. Imports may differ per file; they are listed, not compared.
//
// Usage: movecheck [-allow-doc package.Name ...] BEFORE_DIR AFTER_DIR
//
// -allow-doc names a declaration whose doc comment may change: its body is still compared, and both
// doc comments are printed.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type item struct {
	packageKey string // directory and package name, e.g. "node (package node)"
	name       string // e.g. "func (*Node) adopt"
	file       string // the file it sits in, relative to the tree
	text       string // the canonical text: doc comment, declaration, inner and trailing comments
	body       string // the same without the doc comment
	doc        string
}

type allowList []string

func (list *allowList) String() string         { return strings.Join(*list, ",") }
func (list *allowList) Set(value string) error { *list = append(*list, value); return nil }

func main() {
	var allowDoc allowList
	flag.Var(&allowDoc, "allow-doc", "package.Name whose doc comment may change (repeatable)")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: movecheck [-allow-doc package.Name] BEFORE_DIR AFTER_DIR")
		os.Exit(2)
	}
	beforeItems, beforeImports := collect(flag.Arg(0))
	afterItems, afterImports := collect(flag.Arg(1))

	allowed := map[string]bool{}
	for _, name := range allowDoc {
		allowed[name] = true
	}
	keyOf := func(it item) string {
		if allowed[shortName(it)] {
			return it.packageKey + "\x00" + it.body
		}
		return it.packageKey + "\x00" + it.text
	}

	beforeByKey := map[string][]item{}
	for _, it := range beforeItems {
		beforeByKey[keyOf(it)] = append(beforeByKey[keyOf(it)], it)
	}
	afterByKey := map[string][]item{}
	for _, it := range afterItems {
		afterByKey[keyOf(it)] = append(afterByKey[keyOf(it)], it)
	}

	failures := 0
	var moved, docChanged []string
	keys := map[string]bool{}
	for key := range beforeByKey {
		keys[key] = true
	}
	for key := range afterByKey {
		keys[key] = true
	}
	sortedKeys := make([]string, 0, len(keys))
	for key := range keys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	for _, key := range sortedKeys {
		before, after := beforeByKey[key], afterByKey[key]
		if len(before) != len(after) {
			failures++
			for _, it := range before {
				fmt.Printf("ONLY BEFORE  %s  %s  (%s)\n", it.packageKey, it.name, it.file)
			}
			for _, it := range after {
				fmt.Printf("ONLY AFTER   %s  %s  (%s)\n", it.packageKey, it.name, it.file)
			}
			continue
		}
		sortItems(before)
		sortItems(after)
		for i := range before {
			if before[i].file != after[i].file {
				moved = append(moved, fmt.Sprintf("%-40s %s -> %s", before[i].name, before[i].file, after[i].file))
			}
			if before[i].doc != after[i].doc {
				docChanged = append(docChanged, fmt.Sprintf("%s (%s)\n  before: %q\n  after:  %q",
					before[i].name, after[i].file, before[i].doc, after[i].doc))
			}
		}
	}

	comments := func(items []item) int {
		count := 0
		for _, it := range items {
			if strings.HasSuffix(it.name, "comment") {
				count++
			}
		}
		return count
	}
	fmt.Printf("items: %d before, %d after (declarations %d and %d; package doc and floating comments %d and %d)\n",
		len(beforeItems), len(afterItems), len(beforeItems)-comments(beforeItems), len(afterItems)-comments(afterItems),
		comments(beforeItems), comments(afterItems))
	fmt.Printf("moved: %d\n", len(moved))
	sort.Strings(moved)
	for _, line := range moved {
		fmt.Println("  " + line)
	}
	fmt.Printf("doc comments changed where allowed: %d\n", len(docChanged))
	for _, line := range docChanged {
		fmt.Println("  " + line)
	}

	fmt.Println("imports that differ, by file:")
	files := map[string]bool{}
	for file := range beforeImports {
		files[file] = true
	}
	for file := range afterImports {
		files[file] = true
	}
	sortedFiles := make([]string, 0, len(files))
	for file := range files {
		sortedFiles = append(sortedFiles, file)
	}
	sort.Strings(sortedFiles)
	for _, file := range sortedFiles {
		added, removed := difference(afterImports[file], beforeImports[file]), difference(beforeImports[file], afterImports[file])
		if len(added) > 0 || len(removed) > 0 {
			fmt.Printf("  %s: +%v -%v\n", file, added, removed)
		}
	}

	if failures > 0 {
		fmt.Printf("FAIL: %d declaration texts differ\n", failures)
		os.Exit(1)
	}
	fmt.Println("OK: every declaration and comment is the same text before and after")
}

func shortName(it item) string {
	packageName := it.packageKey[strings.LastIndex(it.packageKey, "package ")+len("package "):]
	packageName = strings.TrimSuffix(packageName, ")")
	fields := strings.Fields(it.name)
	return packageName + "." + fields[len(fields)-1]
}

func sortItems(items []item) {
	sort.Slice(items, func(i, j int) bool { return items[i].file < items[j].file })
}

func difference(from, without map[string]bool) []string {
	var result []string
	for value := range from {
		if !without[value] {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// collect parses every Go file of every package under root, as the go command sees them (it skips
// directories whose names start with "." or "_", and testdata), and returns its items and, per file,
// its imports.
func collect(root string) ([]item, map[string]map[string]bool) {
	var items []item
	imports := map[string]map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "review") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative, _ := filepath.Rel(root, path)
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		packageKey := fmt.Sprintf("%s (package %s)", filepath.Dir(relative), file.Name.Name)
		imports[relative] = map[string]bool{}
		for _, importSpec := range file.Imports {
			text := importSpec.Path.Value
			if importSpec.Name != nil {
				text = importSpec.Name.Name + " " + text
			}
			imports[relative][text] = true
		}

		type span struct{ begin, end token.Pos }
		var spans []span
		for _, declaration := range file.Decls {
			begin, end := declaration.Pos(), declaration.End()
			doc := docOf(declaration)
			if doc != nil {
				begin = doc.Pos()
			}
			for _, group := range file.Comments {
				// a trailing comment on the declaration's last line belongs to it
				if group.Pos() >= declaration.End() && fileSet.Position(group.Pos()).Line == fileSet.Position(declaration.End()).Line {
					end = group.End()
				}
			}
			spans = append(spans, span{begin, end})
			if general, isGeneral := declaration.(*ast.GenDecl); isGeneral && general.Tok == token.IMPORT {
				continue
			}
			text := print(fileSet, declaration, file.Comments)
			var withoutDoc []*ast.CommentGroup
			for _, group := range file.Comments {
				if group != doc {
					withoutDoc = append(withoutDoc, group)
				}
			}
			// The printer prints a node's own Doc when no comment of the list falls in its range, so
			// the body is printed from a copy without its Doc.
			var undocumented ast.Decl = declaration
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				copied := *typed
				copied.Doc = nil
				undocumented = &copied
			case *ast.GenDecl:
				copied := *typed
				copied.Doc = nil
				undocumented = &copied
			}
			body := print(fileSet, undocumented, withoutDoc)
			docText := ""
			if doc != nil {
				docText = doc.Text()
			}
			items = append(items, item{packageKey: packageKey, name: nameOf(fileSet, declaration), file: relative, text: text, body: body, doc: docText})
		}
		// Comments outside every declaration: the package doc and floating comments. They are compared
		// too, so a move cannot drop or edit one.
		for _, group := range file.Comments {
			inside := false
			for _, s := range spans {
				if group.Pos() >= s.begin && group.End() <= s.end {
					inside = true
				}
			}
			if inside {
				continue
			}
			name := "floating comment"
			if group == file.Doc {
				name = "package doc comment"
			}
			text := name + "\n" + group.Text()
			items = append(items, item{packageKey: packageKey, name: name, file: relative, text: text, body: text})
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return items, imports
}

func docOf(declaration ast.Decl) *ast.CommentGroup {
	switch typed := declaration.(type) {
	case *ast.FuncDecl:
		return typed.Doc
	case *ast.GenDecl:
		return typed.Doc
	}
	return nil
}

func print(fileSet *token.FileSet, declaration ast.Decl, comments []*ast.CommentGroup) string {
	var buffer bytes.Buffer
	configuration := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := configuration.Fprint(&buffer, fileSet, &printer.CommentedNode{Node: declaration, Comments: comments}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	return buffer.String()
}

func nameOf(fileSet *token.FileSet, declaration ast.Decl) string {
	switch typed := declaration.(type) {
	case *ast.FuncDecl:
		if typed.Recv != nil && len(typed.Recv.List) > 0 {
			var buffer bytes.Buffer
			printer.Fprint(&buffer, fileSet, typed.Recv.List[0].Type)
			return fmt.Sprintf("func (%s) %s", buffer.String(), typed.Name.Name)
		}
		return "func " + typed.Name.Name
	case *ast.GenDecl:
		var names []string
		for _, spec := range typed.Specs {
			switch spec := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, spec.Name.Name)
			case *ast.ValueSpec:
				for _, name := range spec.Names {
					names = append(names, name.Name)
				}
			}
		}
		return typed.Tok.String() + " " + strings.Join(names, ",")
	}
	return "?"
}
