package main

// html/template files resolve .Name against Go values by reflection at run time, so an exported
// field or method whose name a template uses must keep that name.

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template/parse"
)

// templateFiles lists the template files of the tree: files with a template extension inside a
// directory named "templates" (ui/templates/*.html here), outside review/.
func (t *Tree) templateFiles() ([]string, error) {
	var files []string
	err := filepath.WalkDir(t.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := t.rel(p)
		if d.IsDir() {
			if rel == "review" || strings.HasPrefix(d.Name(), ".") && rel != "" {
				return filepath.SkipDir
			}
			return nil
		}
		switch path.Ext(rel) {
		case ".html", ".tmpl", ".gohtml", ".tpl":
			if path.Base(path.Dir(rel)) == "templates" {
				files = append(files, rel)
			}
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// templateFieldNames returns every name the tree's templates use as a field or method selector
// ({{.Name}}, {{$x.Name}}, {{(pipe).Name}}, {{range .Name}}, ...), mapped to the first template file
// that uses it.
func templateFieldNames(t *Tree) (map[string]string, error) {
	files, err := t.templateFiles()
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, rel := range files {
		src, err := t.read(rel)
		if err != nil {
			return nil, err
		}
		tree := parse.New(rel)
		tree.Mode = parse.SkipFuncCheck // the FuncMap is not needed to find selectors
		set := map[string]*parse.Tree{}
		if _, err := tree.Parse(string(src), "", "", set); err != nil {
			return nil, fmt.Errorf("%s: %v", rel, err)
		}
		set[rel+" (top level)"] = tree
		for _, name := range sortedKeys(set) {
			if set[name].Root != nil {
				walkTemplate(set[name].Root, func(field string) {
					if _, ok := names[field]; !ok {
						names[field] = rel
					}
				})
			}
		}
	}
	return names, nil
}

// walkTemplate calls add for every field or method name selected anywhere under n.
func walkTemplate(n parse.Node, add func(string)) {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return
		}
		for _, c := range n.Nodes {
			walkTemplate(c, add)
		}
	case *parse.ActionNode:
		walkTemplate(n.Pipe, add)
	case *parse.PipeNode:
		if n == nil {
			return
		}
		for _, d := range n.Decl {
			walkTemplate(d, add)
		}
		for _, c := range n.Cmds {
			walkTemplate(c, add)
		}
	case *parse.CommandNode:
		for _, a := range n.Args {
			walkTemplate(a, add)
		}
	case *parse.FieldNode: // .A.B
		for _, id := range n.Ident {
			add(id)
		}
	case *parse.ChainNode: // (pipe).A.B
		walkTemplate(n.Node, add)
		for _, id := range n.Field {
			add(id)
		}
	case *parse.VariableNode: // $x.A.B
		for _, id := range n.Ident[1:] {
			add(id)
		}
	case *parse.IfNode:
		walkBranch(&n.BranchNode, add)
	case *parse.RangeNode:
		walkBranch(&n.BranchNode, add)
	case *parse.WithNode:
		walkBranch(&n.BranchNode, add)
	case *parse.TemplateNode:
		walkTemplate(n.Pipe, add)
	}
}

func walkBranch(b *parse.BranchNode, add func(string)) {
	walkTemplate(b.Pipe, add)
	walkTemplate(b.List, add)
	walkTemplate(b.ElseList, add)
}
