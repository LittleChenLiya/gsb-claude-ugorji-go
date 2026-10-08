// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// ---- two-level embedding fixtures ----------------------------------------

type sfpInnerA struct {
	Name   string
	OnlyA  string
	Tagged string `codec:"alias"`
}

type sfpInnerB struct {
	Name  string
	OnlyB string
}

// sfpAmbiguous embeds two structs at the same (first) level which both
// promote a Name field at the same depth.
type sfpAmbiguous struct {
	sfpInnerA
	sfpInnerB
	Own string
}

// sfpShallow wins unambiguously: its own Name (depth 0) shadows the
// promoted Name fields at depth 1.
type sfpShallow struct {
	Name string
	sfpInnerA
	sfpInnerB
}

// sfpTagTie: both embedded fields explicitly rename a field to "tie" at
// the same depth. Explicit tags follow the same resolution rules.
type sfpTagTie struct {
	sfpTagTieA
	sfpTagTieB
}

type sfpTagTieA struct {
	X string `codec:"tie"`
}
type sfpTagTieB struct {
	Y string `codec:"tie"`
}

var (
	sfpJSONMarshalCalls   int
	sfpSelferMarshalCalls int
)

type sfpJSONMarshaler struct {
	sfpAmbiguous
}

func (sfpJSONMarshaler) MarshalJSON() ([]byte, error) {
	sfpJSONMarshalCalls++
	return []byte(`{"custom":true}`), nil
}

func (*sfpJSONMarshaler) UnmarshalJSON([]byte) error { return nil }

type sfpSelfer struct {
	sfpAmbiguous
}

func (*sfpSelfer) CodecEncodeSelf(e *Encoder) {
	sfpSelferMarshalCalls++
	e.MustEncode(map[string]bool{"custom": true})
}

func (*sfpSelfer) CodecDecodeSelf(*Decoder) {}

func sfpResetCounters() {
	sfpJSONMarshalCalls = 0
	sfpSelferMarshalCalls = 0
}

func sfpFindConflict(t *testing.T, tinfos *TypeInfos, typ reflect.Type, name string) StructFieldConflict {
	t.Helper()
	cfs := tinfos.StructFieldConflicts(typ)
	for _, cf := range cfs {
		if cf.Name == name {
			return cf
		}
	}
	t.Fatalf("no conflict for name %q on %v; got %+v", name, typ, cfs)
	return StructFieldConflict{}
}

func sfpCandPaths(cf StructFieldConflict) []string {
	out := make([]string, len(cf.Candidates))
	for i, c := range cf.Candidates {
		out[i] = c.pathString()
	}
	return out
}

func TestStructFieldPrecheckAmbiguity(t *testing.T) {
	ti := NewTypeInfos([]string{"codec", "json"})
	typ := reflect.TypeOf(sfpAmbiguous{})

	cfs := ti.StructFieldConflicts(typ)
	if len(cfs) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", cfs)
	}
	cf := sfpFindConflict(t, ti, typ, "Name")
	paths := sfpCandPaths(cf)
	want := []string{"sfpInnerA.Name", "sfpInnerB.Name"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("candidate paths = %v, want %v", paths, want)
	}
	for _, c := range cf.Candidates {
		if c.Depth != 1 {
			t.Fatalf("candidate %s depth = %d, want 1", c.pathString(), c.Depth)
		}
		if c.Field != "Name" {
			t.Fatalf("candidate field = %q, want Name", c.Field)
		}
		if len(c.Path) != 1 || c.Path[0] == "" {
			t.Fatalf("candidate %s path = %v, want single embedded node", c.pathString(), c.Path)
		}
	}
	// The library routes the name to the first tied candidate; the rest
	// are shadowed.
	if !cf.Candidates[0].Selected {
		t.Fatalf("first tied candidate %s must be Selected", cf.Candidates[0].pathString())
	}
	for i := 1; i < len(cf.Candidates); i++ {
		if cf.Candidates[i].Selected {
			t.Fatalf("shadowed candidate %s must not be Selected", cf.Candidates[i].pathString())
		}
	}
	// Index chain must reach the same field via reflection.
	for _, c := range cf.Candidates {
		f := typ.FieldByIndex(c.Index)
		if f.Name != "Name" {
			t.Fatalf("index %v did not resolve to Name, got %q", c.Index, f.Name)
		}
	}
}

func TestStructFieldPrecheckShallowWins(t *testing.T) {
	ti := NewTypeInfos([]string{"codec", "json"})
	if cfs := ti.StructFieldConflicts(reflect.TypeOf(sfpShallow{})); len(cfs) != 0 {
		t.Fatalf("expected no conflicts when own field shadows promoted ones, got %+v", cfs)
	}
	if cfs := ti.StructFieldConflicts(reflect.TypeOf(sfpTagTieA{})); len(cfs) != 0 {
		t.Fatalf("single tagged field must not be a conflict, got %+v", cfs)
	}
}

func TestStructFieldPrecheckExplicitTagTie(t *testing.T) {
	ti := NewTypeInfos([]string{"codec", "json"})
	cf := sfpFindConflict(t, ti, reflect.TypeOf(sfpTagTie{}), "tie")
	paths := sfpCandPaths(cf)
	want := []string{"sfpTagTieA.X", "sfpTagTieB.Y"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("tag tie paths = %v, want %v", paths, want)
	}
	for _, c := range cf.Candidates {
		if !c.ExplicitTag {
			t.Fatalf("candidate %s should report ExplicitTag=true", c.pathString())
		}
	}
}

func TestStructFieldPrecheckIsStatic(t *testing.T) {
	// The precheck must not invoke Marshal extensions.
	sfpResetCounters()
	ti := NewTypeInfos([]string{"codec", "json"})
	_ = ti.StructFieldConflicts(reflect.TypeOf(sfpJSONMarshaler{}))
	_ = ti.StructFieldConflicts(reflect.TypeOf(sfpSelfer{}))
	if sfpJSONMarshalCalls != 0 || sfpSelferMarshalCalls != 0 {
		t.Fatalf("precheck invoked marshal extensions: json=%d selfer=%d",
			sfpJSONMarshalCalls, sfpSelferMarshalCalls)
	}
	// Pointer-to-struct is dereferenced; non-struct types return nothing.
	if cfs := ti.StructFieldConflicts(reflect.TypeOf((*sfpAmbiguous)(nil))); len(cfs) != 1 {
		t.Fatalf("ptr-to-struct expected 1 conflict, got %+v", cfs)
	}
	if cfs := ti.StructFieldConflicts(reflect.TypeOf(0)); len(cfs) != 0 {
		t.Fatalf("non-struct got conflicts %+v", cfs)
	}
	if cfs := ti.StructFieldConflicts(nil); len(cfs) != 0 {
		t.Fatalf("nil type got conflicts %+v", cfs)
	}
}

func TestStructFieldStrictRejectsAndRoutesJSON(t *testing.T) {
	sfpResetCounters()

	strictH := new(JsonHandle)
	strictH.StructFieldsStrict = true
	lenientH := new(JsonHandle)

	// 1. Strict mode rejects before encoding and without calling MarshalJSON.
	var out []byte
	err := NewEncoderBytes(&out, strictH).Encode(&sfpJSONMarshaler{})
	if err == nil {
		t.Fatalf("strict encode should have been rejected")
	}
	var cfErr *StructFieldConflictError
	if !errors.As(err, &cfErr) {
		t.Fatalf("expected *StructFieldConflictError, got %T: %v", err, err)
	}
	if len(out) != 0 {
		t.Fatalf("strict encode wrote bytes before rejecting: %s", out)
	}
	if sfpJSONMarshalCalls != 0 {
		t.Fatalf("MarshalJSON called %d times on strict rejection", sfpJSONMarshalCalls)
	}
	if !strings.Contains(err.Error(), "Name") ||
		!strings.Contains(err.Error(), "sfpInnerA.Name") ||
		!strings.Contains(err.Error(), "sfpInnerB.Name") {
		t.Fatalf("error message lacks candidate paths: %v", err)
	}

	// Also reject through an io.Writer-backed encoder.
	sfpResetCounters()
	var buf bytes.Buffer
	err = NewEncoder(&buf, strictH).Encode(sfpAmbiguous{
		sfpInnerA: sfpInnerA{Name: "a"},
		sfpInnerB: sfpInnerB{Name: "b"},
		Own:       "own",
	})
	if err == nil {
		t.Fatalf("strict encode via io.Writer should have been rejected")
	}
	var cfErr2 *StructFieldConflictError
	if !errors.As(err, &cfErr2) {
		t.Fatalf("expected *StructFieldConflictError, got %T: %v", err, err)
	}
	if buf.Len() != 0 || sfpJSONMarshalCalls != 0 {
		t.Fatalf("writer strict path wrote bytes=%d or called marshaler=%d",
			buf.Len(), sfpJSONMarshalCalls)
	}

	// 2. Lenient mode encodes the same type. The name still routes to the
	//    first tied candidate (sfpInnerA.Name); sfpInnerB.Name is shadowed.
	out = nil
	if err := NewEncoderBytes(&out, lenientH).Encode(sfpAmbiguous{
		sfpInnerA: sfpInnerA{Name: "a", OnlyA: "A", Tagged: "t"},
		sfpInnerB: sfpInnerB{Name: "b", OnlyB: "B"},
		Own:       "own",
	}); err != nil {
		t.Fatalf("lenient encode error: %v", err)
	}
	got := string(out)
	for _, want := range []string{`"Name":"a"`, `"OnlyA":"A"`, `"OnlyB":"B"`, `"Own":"own"`, `"alias":"t"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("lenient output missing %s: %s", want, got)
		}
	}
	if strings.Contains(got, `"Name":"b"`) {
		t.Fatalf("shadowed sfpInnerB.Name must not be encoded: %s", got)
	}

	// 3. Shallow winner routes correctly and is allowed even in strict mode.
	out = nil
	if err := NewEncoderBytes(&out, strictH).Encode(sfpShallow{
		Name:      "top",
		sfpInnerA: sfpInnerA{Name: "inner-a"},
	}); err != nil {
		t.Fatalf("strict encode of unambiguous type failed: %v", err)
	}
	if !strings.Contains(string(out), `"Name":"top"`) {
		t.Fatalf("shallow Name not routed from top field: %s", out)
	}

	// Sanity: without strict mode, a Marshaler on an ambiguous type runs
	// (exactly once) - strict mode is the only thing suppressing it.
	sfpResetCounters()
	var mout []byte
	if err := NewEncoderBytes(&mout, lenientH).Encode(&sfpJSONMarshaler{}); err != nil {
		t.Fatalf("lenient marshaler encode error: %v", err)
	}
	if sfpJSONMarshalCalls != 1 {
		t.Fatalf("MarshalJSON calls = %d, want 1 in lenient mode", sfpJSONMarshalCalls)
	}
	if string(mout) != `{"custom":true}` {
		t.Fatalf("lenient marshaler output = %s", mout)
	}
}

func TestStructFieldStrictRejectsSelferMsgpack(t *testing.T) {
	// Binary formats dispatch to Selfer before struct encoding; the strict
	// gate must still run first, so the custom encoder is never invoked.
	sfpResetCounters()
	h := new(MsgpackHandle)
	h.StructFieldsStrict = true

	var out []byte
	err := NewEncoderBytes(&out, h).Encode(&sfpSelfer{})
	if err == nil {
		t.Fatalf("strict msgpack encode should have been rejected")
	}
	var cfErr *StructFieldConflictError
	if !errors.As(err, &cfErr) {
		t.Fatalf("expected *StructFieldConflictError, got %T: %v", err, err)
	}
	if len(out) != 0 {
		t.Fatalf("strict msgpack wrote bytes: %x", out)
	}
	if sfpSelferMarshalCalls != 0 {
		t.Fatalf("CodecEncodeSelf called %d times on strict rejection", sfpSelferMarshalCalls)
	}

	// Sanity: without strict mode, the Selfer runs exactly once.
	sfpResetCounters()
	lenient := new(MsgpackHandle)
	if err := NewEncoderBytes(&out, lenient).Encode(&sfpSelfer{}); err != nil {
		t.Fatalf("lenient msgpack encode error: %v", err)
	}
	if sfpSelferMarshalCalls != 1 {
		t.Fatalf("CodecEncodeSelf calls = %d, want 1 in lenient mode", sfpSelferMarshalCalls)
	}
}

func TestStructFieldPrecheckSelectionMatchesResolve(t *testing.T) {
	// Three-way tie at depth 1: the first declared embedded field must win
	// both in the precheck report and in the actual encoding.
	type M struct{ Name string }
	type N struct{ Name string }
	type P struct{ Name string }
	type Triple struct {
		M
		N
		P
	}
	ti := NewTypeInfos([]string{"codec", "json"})
	cfs := ti.StructFieldConflicts(reflect.TypeOf(Triple{}))
	if len(cfs) != 1 || len(cfs[0].Candidates) != 3 {
		t.Fatalf("unexpected conflicts: %+v", cfs)
	}
	if cfs[0].Candidates[0].pathString() != "M.Name" || !cfs[0].Candidates[0].Selected {
		t.Fatalf("winner should be M.Name: %+v", cfs[0].Candidates)
	}

	h := new(JsonHandle)
	var out []byte
	if err := NewEncoderBytes(&out, h).Encode(Triple{
		M: M{Name: "m"}, N: N{Name: "n"}, P: P{Name: "p"},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(out), `"Name":"m"`) {
		t.Fatalf("resolve winner mismatch, want M value, got %s", out)
	}

	// Pointer embedded fields are still promoted and reported.
	type Q struct{ Name string }
	type ViaPtr struct{ *Q }
	type ViaPtr2 struct{ *Q }
	type BothPtr struct {
		ViaPtr
		ViaPtr2
	}
	cfs = ti.StructFieldConflicts(reflect.TypeOf(BothPtr{}))
	if len(cfs) != 1 {
		t.Fatalf("pointer-embed expected 1 conflict, got %+v", cfs)
	}
	if got := cfs[0].Candidates[0].pathString(); got != "ViaPtr.Q.Name" {
		t.Fatalf("pointer-embed path wrong: %s", got)
	}
	if cfs[0].Candidates[0].Depth != 2 {
		t.Fatalf("pointer-embed depth wrong: %d", cfs[0].Candidates[0].Depth)
	}
}

func TestStructFieldStrictNestedStruct(t *testing.T) {
	// A nested ambiguous struct (not the top-level type) is also rejected
	// before its value is encoded.
	h := new(JsonHandle)
	h.StructFieldsStrict = true
	type Wrap struct {
		Inner sfpAmbiguous
		Other int
	}
	var out []byte
	err := NewEncoderBytes(&out, h).Encode(Wrap{
		Inner: sfpAmbiguous{
			sfpInnerA: sfpInnerA{Name: "a"},
			sfpInnerB: sfpInnerB{Name: "b"},
		},
		Other: 1,
	})
	if err == nil {
		t.Fatalf("expected nested ambiguity rejection, got output %s", out)
	}
	var cfErr *StructFieldConflictError
	if !errors.As(err, &cfErr) {
		t.Fatalf("expected *StructFieldConflictError, got %T: %v", err, err)
	}
	if len(out) != 0 {
		t.Fatalf("strict encode wrote partial output: %s", out)
	}
}

var _ io.Writer = (*bytes.Buffer)(nil)

// ExampleStructFieldConflicts demonstrates the embedded-field ambiguity
// pre-check on an in-memory, two-level embedding fixture, and the strict
// mode rejection that encodes nothing and never reaches a Marshaler.
func ExampleTypeInfos_StructFieldConflicts() {
	type L1 struct {
		Name string
	}
	type R1 struct {
		Name string
	}
	type Both struct {
		L1
		R1
	}

	// Static pre-check: no value is encoded, no Marshal extension runs.
	for _, cf := range defTypeInfos.StructFieldConflicts(reflect.TypeOf(Both{})) {
		fmt.Println("ambiguous name:", cf.Name)
		for _, c := range cf.Candidates {
			fmt.Printf("  %s (depth=%d selected=%t)\n", c.pathString(), c.Depth, c.Selected)
		}
	}

	// Strict mode rejects before writing any byte.
	h := new(JsonHandle)
	h.StructFieldsStrict = true
	var out []byte
	err := NewEncoderBytes(&out, h).Encode(Both{})
	var cfErr *StructFieldConflictError
	fmt.Println("rejected:", errors.As(err, &cfErr))
	fmt.Println("bytes written:", len(out))
	// Output:
	// ambiguous name: Name
	//   L1.Name (depth=1 selected=true)
	//   R1.Name (depth=1 selected=false)
	// rejected: true
	// bytes written: 0
}
