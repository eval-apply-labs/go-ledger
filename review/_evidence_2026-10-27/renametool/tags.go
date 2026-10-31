package main

// Struct tags and JSON. encoding/json names an object member after the field's json tag name or,
// when there is none, after the Go field name. Renaming an exported field therefore changes the
// JSON it produces and accepts, unless the tag names the old key. apply adds that tag; check
// verifies that every struct's JSON members are unchanged.
//
// The model below follows encoding/json as built by go1.27.3, which runs on the v2 engine
// (GOEXPERIMENT jsonv2 is on by default) in v1 mode; see makeStructFields and parseFieldOptions
// in GOROOT/src/encoding/json/v2/fields.go.

import (
	"go/types"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// tagPair is one key:"value" pair of a struct tag, with the byte span of its quoted value.
type tagPair struct {
	key        string
	value      string // the quoted value as written, e.g. "\"name,omitempty\""
	start, end int    // span of value within the tag
}

// parseTag splits a struct tag into its key:"value" pairs following the same loop as
// reflect.StructTag.Lookup. ok is false when the tag is not in that conventional format (Lookup
// stops at the first malformed pair, and so does parseTag).
func parseTag(tag string) (pairs []tagPair, ok bool) {
	offset := 0
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag, offset = tag[i:], offset+i
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			return pairs, false
		}
		key := tag[:i]
		tag, offset = tag[i+1:], offset+i+1
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			return pairs, false
		}
		pairs = append(pairs, tagPair{key: key, value: tag[:i+1], start: offset, end: offset + i + 1})
		tag, offset = tag[i+1:], offset+i+1
	}
	return pairs, true
}

// setTagValue returns tag with the value of key set to value: the first key:"..." pair is
// rewritten in place, or the pair is appended. Every other pair is kept byte for byte.
func setTagValue(tag, key, value string) string {
	pairs, _ := parseTag(tag)
	for _, p := range pairs {
		if p.key == key {
			return tag[:p.start] + strconv.Quote(value) + tag[p.end:]
		}
	}
	if tag != "" && !strings.HasSuffix(tag, " ") {
		tag += " "
	}
	return tag + key + ":" + strconv.Quote(value)
}

// otherTagKeys renders every pair except json, for comparing the tag keys apply never touches.
// A tag that does not parse is returned whole, so any change to it is reported.
func otherTagKeys(tag string) string {
	pairs, ok := parseTag(tag)
	if !ok {
		return "unparsable tag " + strconv.Quote(tag)
	}
	var parts []string
	for _, p := range pairs {
		if p.key != "json" {
			parts = append(parts, p.key+":"+p.value)
		}
	}
	return strings.Join(parts, " ")
}

// jsonView is how encoding/json treats one struct field.
type jsonView struct {
	Ignored   bool   // json:"-", or an unexported field that is not an embedded struct
	Flattened bool   // an embedded struct without a JSON name: its own fields are promoted
	Key       string // the member name otherwise: the tag's name, or the Go field name
	Options   string // what follows the name in the json tag, e.g. ",omitempty"
	HasName   bool   // the json tag names the member explicitly
}

// effective drops HasName, which on its own does not change the JSON (it only breaks ties between
// equally deep members with the same name; jsonMembers models that).
func (v jsonView) effective() jsonView { v.HasName = false; return v }

func jsonViewOf(field *types.Var, tag string) jsonView {
	value, _ := reflect.StructTag(tag).Lookup("json")
	if value == "-" {
		return jsonView{Ignored: true}
	}
	if !field.Exported() && !field.Embedded() {
		return jsonView{Ignored: true}
	}
	v := jsonView{Key: field.Name(), Options: value}
	if value != "" && !strings.HasPrefix(value, ",") {
		if strings.ContainsAny(value, "\\'\"`") {
			// A quoted or escaped name: compare the raw tag instead of modelling the grammar.
			v.Key, v.Options, v.HasName = value, "", true
		} else {
			name, opts, _ := strings.Cut(value, ",")
			v.Key, v.HasName = name, true
			if opts != "" {
				v.Options = "," + opts
			} else {
				v.Options = ""
			}
		}
	}
	if field.Embedded() && !v.HasName && embeddedStruct(field.Type()) != nil {
		return jsonView{Flattened: true, Options: v.Options}
	}
	if !field.Exported() && !(field.Embedded() && embeddedStruct(field.Type()) != nil) {
		return jsonView{Ignored: true} // an unexported embedded non-struct
	}
	return v
}

// embeddedStruct returns the struct an embedded field promotes fields from: its type, or the
// element of an unnamed pointer type, when that is a struct; nil otherwise.
func embeddedStruct(typ types.Type) *types.Struct {
	if p, ok := types.Unalias(typ).(*types.Pointer); ok {
		typ = p.Elem()
	}
	st, _ := typ.Underlying().(*types.Struct)
	return st
}

// jsonMember is one member of a struct's JSON object.
type jsonMember struct {
	Key     string
	Index   []int // field index path, through embedded structs
	Options string
}

// jsonMembers lists the members of a struct's JSON object as encoding/json resolves them: a
// breadth-first walk through embedded structs, then, among fields with the same name, only the
// dominant one is kept (the shallowest; at equal depth the one named by its tag; otherwise none).
//
// check passes the struct type literal as root, where encoding/json starts from the named type.
// For a struct that embeds itself (type T struct{ *T }) that only lets the walk go one level
// deeper, adding deeper copies of names already present, which never dominate: same result.
func jsonMembers(root types.Type) []jsonMember {
	type entry struct {
		typ   types.Type
		index []int
		visit bool // whether to descend into this struct's embedded structs
	}
	type candidate struct {
		jsonMember
		hasName bool
	}
	var seen []types.Type
	isSeen := func(t types.Type) bool {
		for _, s := range seen {
			if types.Identical(s, t) {
				return true
			}
		}
		return false
	}
	queue := []entry{{root, nil, true}}
	seen = append(seen, root)
	var all []candidate
	for qi := 0; qi < len(queue); qi++ {
		e := queue[qi]
		st, ok := e.typ.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for i := 0; i < st.NumFields(); i++ {
			field := st.Field(i)
			view := jsonViewOf(field, st.Tag(i))
			if view.Ignored {
				continue
			}
			index := append(append([]int(nil), e.index...), i)
			if view.Flattened {
				inner := field.Type()
				if p, ok := types.Unalias(inner).(*types.Pointer); ok {
					inner = p.Elem()
				}
				if e.visit {
					queue = append(queue, entry{inner, index, !isSeen(inner)})
				}
				seen = append(seen, inner)
				continue
			}
			all = append(all, candidate{jsonMember{view.Key, index, view.Options}, view.HasName})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		x, y := all[i], all[j]
		if x.Key != y.Key {
			return x.Key < y.Key
		}
		if len(x.Index) != len(y.Index) {
			return len(x.Index) < len(y.Index)
		}
		return x.hasName && !y.hasName
	})
	var members []jsonMember
	for len(all) > 0 {
		n := 1
		for n < len(all) && all[n].Key == all[0].Key {
			n++
		}
		if n == 1 || len(all[0].Index) != len(all[1].Index) || all[0].hasName != all[1].hasName {
			members = append(members, all[0].jsonMember)
		}
		all = all[n:]
	}
	sort.Slice(members, func(i, j int) bool { return lessIndex(members[i].Index, members[j].Index) })
	return members
}

func lessIndex(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func sameMembers(a, b []jsonMember) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Options != b[i].Options || !equalInts(a[i].Index, b[i].Index) {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func memberKeys(ms []jsonMember) string {
	var keys []string
	for _, m := range ms {
		keys = append(keys, m.Key+m.Options)
	}
	return strings.Join(keys, " ")
}
