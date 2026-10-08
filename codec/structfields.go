// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"reflect"
	"sort"
	"strings"
)

// StructFieldCandidate describes one concrete struct field that
// contributes to an encoded field name.
//
// A candidate is either a field declared directly on a struct, or a
// field promoted into the struct through one or more anonymous
// (embedded) fields.
type StructFieldCandidate struct {
	// Field is the Go name of the candidate field itself.
	Field string

	// Path is the sequence of Go field names walked from the
	// inspected struct to reach Field.
	//
	// For a field declared directly on the inspected struct,
	// Path is nil. For a promoted field, Path lists the embedded
	// fields traversed, outermost first.
	Path []string

	// Index is the reflect index chain corresponding to Path and Field,
	// usable with reflect.Type.FieldByIndex.
	Index []int

	// Depth is the number of embedded fields traversed from the
	// inspected struct. It is 0 for a directly declared field.
	Depth int

	// ExplicitTag reports whether the encoded name was set explicitly
	// via a struct tag on the candidate field.
	ExplicitTag bool

	// Selected reports whether the library routes the encoded name to
	// this candidate.
	//
	// Per the library's selector rules, the first candidate (in struct
	// declaration order) at the shallowest tied depth wins the name;
	// the other tied candidates are shadowed. All tied candidates are
	// still reported as an ambiguity. Exactly one candidate of a
	// reported conflict has Selected set to true.
	Selected bool
}

// pathString returns the fully-qualified selector path of the candidate,
// e.g. Outer.Inner.Name.
func (c StructFieldCandidate) pathString() string {
	if len(c.Path) == 0 {
		return c.Field
	}
	n := 0
	for _, p := range c.Path {
		n += len(p) + 1
	}
	n += len(c.Field)
	b := make([]byte, 0, n)
	for _, p := range c.Path {
		b = append(b, p...)
		b = append(b, '.')
	}
	b = append(b, c.Field...)
	return string(b)
}

// StructFieldConflict reports an ambiguous encoded field name on a struct:
// multiple fields at the same (shallowest) depth contribute the same name.
//
// Per the library's selector rules, the first candidate (in declaration
// order) still wins the name; the others are shadowed. Candidates[0] is
// therefore the field the value is actually routed to, and its Selected
// field is true.
type StructFieldConflict struct {
	// Name is the encoded name (struct tag name or Go field name)
	// that has multiple competing candidates.
	Name string

	// Candidates are the competing fields at the same shallowest depth,
	// in declaration order. The first one (Selected=true) is the field
	// the library encodes the name from; the rest are shadowed.
	Candidates []StructFieldCandidate
}

// candidatePathNames resolves the Go field names along an index chain
// rooted at rt. It returns the traversed embedded field names and the
// final leaf field name.
func candidatePathNames(rt reflect.Type, index []int) (path []string, leaf string) {
	cur := rt
	for k, i := range index {
		f := cur.Field(i)
		if k == len(index)-1 {
			leaf = f.Name
			return
		}
		path = append(path, f.Name)
		cur = f.Type
		for cur.Kind() == reflect.Ptr {
			cur = cur.Elem()
		}
	}
	return
}

// computeStructFieldConflicts derives the field mapping ambiguities from
// the raw set of resolved candidates produced by rget.
//
// It mirrors the library's selector rules exactly:
//   - a candidate at a shallower depth shadows all deeper candidates,
//   - if multiple candidates tie at the shallowest depth, the name is
//     ambiguous; the first of the tied candidates (in declaration order)
//     is the one the library actually routes the name to, while the
//     others are shadowed. All tied candidates are still reported.
//
// Candidates which lose to a shallower field are not reported,
// consistent with the encoding behavior.
func computeStructFieldConflicts(rt reflect.Type, x []structFieldInfo) []StructFieldConflict {
	if len(x) < 2 {
		return nil
	}
	type group struct {
		name string
		sfis []int
	}
	var groups []group
	byName := make(map[string]int, len(x))
	for i := range x {
		if x[i].encName == "" {
			continue
		}
		g, ok := byName[x[i].encName]
		if !ok {
			byName[x[i].encName] = len(groups)
			groups = append(groups, group{name: x[i].encName, sfis: []int{i}})
			continue
		}
		groups[g].sfis = append(groups[g].sfis, i)
	}
	var conflicts []StructFieldConflict
	for _, grp := range groups {
		if len(grp.sfis) < 2 {
			continue
		}
		minDepth := len(x[grp.sfis[0]].parents)
		for _, i := range grp.sfis[1:] {
			if d := len(x[i].parents); d < minDepth {
				minDepth = d
			}
		}
		var tied []int
		for _, i := range grp.sfis {
			if len(x[i].parents) == minDepth {
				tied = append(tied, i)
			}
		}
		if len(tied) < 2 {
			continue
		}
		cf := StructFieldConflict{Name: grp.name}
		for k, i := range tied {
			sfi := &x[i]
			cand := StructFieldCandidate{
				Depth:       len(sfi.parents),
				ExplicitTag: sfi.explicitTag,
				Selected:    k == 0, // first tied candidate wins (see typeInfo.resolve)
				Index:       make([]int, 0, len(sfi.parents)+1),
			}
			for p := range sfi.parents {
				cand.Index = append(cand.Index, int(sfi.parents[p].index))
			}
			cand.Index = append(cand.Index, int(sfi.node.index))
			cand.Path, cand.Field = candidatePathNames(rt, cand.Index)
			cf.Candidates = append(cf.Candidates, cand)
		}
		conflicts = append(conflicts, cf)
	}
	if len(conflicts) == 0 {
		return nil
	}
	sort.Slice(conflicts, func(a, b int) bool {
		return conflicts[a].Name < conflicts[b].Name
	})
	return conflicts
}

// StructFieldConflicts pre-checks how the fields of a struct type are
// mapped for encoding and decoding, and reports any ambiguous names.
//
// t must be a struct type or a pointer to a struct type; otherwise
// nil is returned.
//
// The check is a pure static inspection using the same field discovery
// and selection rules as the encoder/decoder. It does not encode the
// value and never invokes Marshal extensions (Selfer, codec extensions,
// encoding.Text/Binary/JSON Marshaler, etc).
//
// Each returned conflict names the competing fields and the actual
// selector path of every candidate at the tied (shallowest) depth.
// A field name for which an unambiguous shallower candidate exists
// is not reported, and names set explicitly via struct tags follow
// the same rules as during normal encoding.
func (x *TypeInfos) StructFieldConflicts(t reflect.Type) []StructFieldConflict {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	return x.get(rt2id(t), t).structConflicts
}

// StructFieldConflictError is returned during encoding in strict mode
// when a struct has ambiguously mapped fields.
type StructFieldConflictError struct {
	// Type is the struct type with the ambiguous field mapping.
	Type reflect.Type

	// Conflicts are the ambiguous names and their candidate paths.
	Conflicts []StructFieldConflict
}

func (e *StructFieldConflictError) Error() string {
	var b strings.Builder
	b.WriteString("codec: ambiguous field mapping for ")
	b.WriteString(e.Type.String())
	b.WriteString(": ")
	for i, c := range e.Conflicts {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(`"`)
		b.WriteString(c.Name)
		b.WriteString(`" reached from: `)
		for j, cand := range c.Candidates {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(cand.pathString())
		}
	}
	return b.String()
}
