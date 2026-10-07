// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// ---- fixtures: two-level embedding with same-name fields

type tEmbAmbInner struct {
	Name string
}

type tEmbAmbMidA struct {
	tEmbAmbInner
}

type tEmbAmbMidB struct {
	tEmbAmbInner
}

// tEmbAmbOuter has Name promoted via 2 paths at the same depth (2).
type tEmbAmbOuter struct {
	tEmbAmbMidA
	tEmbAmbMidB
}

// ---- fixtures: one-level (flat) embedding with same-name fields

type tEmbAmbFlatA struct {
	ID int
}

type tEmbAmbFlatB struct {
	ID string
}

type tEmbAmbFlat struct {
	tEmbAmbFlatA
	tEmbAmbFlatB
}

// ---- fixtures: pointer embedded fields
//
// Note: embedded pointer types must be exported to be inlined
// (an unexported embedded pointer field cannot be set during decode).

type TEmbPtrInnerA struct {
	Name string
}

type TEmbPtrInnerB struct {
	Name string
}

type tEmbAmbPtr struct {
	*TEmbPtrInnerA
	TEmbPtrInnerB
}

// ---- fixtures: explicit tags decide the encoded name

type tEmbTagA struct {
	F1 int `codec:"Same"`
}

type tEmbTagB struct {
	F2 string `codec:"Same"`
}

// tEmbTagOuter has a conflict on the explicit tag name "Same".
type tEmbTagOuter struct {
	tEmbTagA
	tEmbTagB
}

// tEmbTagNoConfOuter: explicit tag renames one side, so no conflict.
type tEmbTagNoConfOuter struct {
	tEmbTagA
	TEmbPtrInnerB // Name string
}

// ---- fixtures: shallower depth wins, so no ambiguity

type tEmbShadowOuter struct {
	tEmbAmbInner        // Name at depth 1
	Name         string `codec:"Name"` // depth 0 wins
}

// ---- fixtures: custom marshaler with a call counter

type tEmbCountingMarshaler struct {
	n *int
}

func (t tEmbCountingMarshaler) MarshalJSON() ([]byte, error) {
	*t.n++
	return []byte(`"cm"`), nil
}

func (t tEmbCountingMarshaler) UnmarshalJSON([]byte) error { return nil }

type tEmbMarshA struct {
	M tEmbCountingMarshaler
}

type tEmbMarshB struct {
	M tEmbCountingMarshaler
}

// tEmbMarshOuter has M promoted via 2 paths at the same depth (1).
type tEmbMarshOuter struct {
	tEmbMarshA
	tEmbMarshB
}

// ---- helpers

func testEmbAmbCheck(t *testing.T, h Handle, v interface{}, want []StructFieldAmbiguity) {
	t.Helper()
	bh := h.getBasicHandle()
	got := bh.CheckStructFieldAmbiguity(v)
	if len(got) != len(want) {
		t.Fatalf("CheckStructFieldAmbiguity(%T): got %v, want %v", v, got, want)
	}
	for i := range want {
		if got[i].Name != want[i].Name || !reflect.DeepEqual(got[i].Paths, want[i].Paths) {
			t.Fatalf("CheckStructFieldAmbiguity(%T)[%d]: got %v, want %v", v, i, got[i], want[i])
		}
	}
}

func TestEmbeddedFieldAmbiguityPrecheck(t *testing.T) {
	h := &JsonHandle{}

	// two-level embedding: same-name field via 2 candidate paths at depth 2
	testEmbAmbCheck(t, h, tEmbAmbOuter{}, []StructFieldAmbiguity{
		{Name: "Name", Paths: []string{"tEmbAmbMidA.tEmbAmbInner.Name", "tEmbAmbMidB.tEmbAmbInner.Name"}},
	})

	// pointer to the struct type is dereferenced
	testEmbAmbCheck(t, h, &tEmbAmbOuter{}, []StructFieldAmbiguity{
		{Name: "Name", Paths: []string{"tEmbAmbMidA.tEmbAmbInner.Name", "tEmbAmbMidB.tEmbAmbInner.Name"}},
	})

	// one-level embedding
	testEmbAmbCheck(t, h, tEmbAmbFlat{}, []StructFieldAmbiguity{
		{Name: "ID", Paths: []string{"tEmbAmbFlatA.ID", "tEmbAmbFlatB.ID"}},
	})

	// pointer embedded fields
	testEmbAmbCheck(t, h, tEmbAmbPtr{}, []StructFieldAmbiguity{
		{Name: "Name", Paths: []string{"TEmbPtrInnerA.Name", "TEmbPtrInnerB.Name"}},
	})

	// explicit tag name is used for the conflict (tag takes precedence over field name)
	testEmbAmbCheck(t, h, tEmbTagOuter{}, []StructFieldAmbiguity{
		{Name: "Same", Paths: []string{"tEmbTagA.F1", "tEmbTagB.F2"}},
	})

	// no false positives:
	//   - explicit tag renames one side, so names differ
	//   - shallower (direct) field shadows the promoted one
	//   - non-struct types
	testEmbAmbCheck(t, h, tEmbTagNoConfOuter{}, nil)
	testEmbAmbCheck(t, h, tEmbShadowOuter{}, nil)
	testEmbAmbCheck(t, h, 42, nil)
	testEmbAmbCheck(t, h, nil, nil)

	// the precheck never calls any Marshaler extension
	var n int
	v := tEmbMarshOuter{}
	v.tEmbMarshA.M.n = &n
	v.tEmbMarshB.M.n = &n
	testEmbAmbCheck(t, h, v, []StructFieldAmbiguity{
		{Name: "M", Paths: []string{"tEmbMarshA.M", "tEmbMarshB.M"}},
	})
	if n != 0 {
		t.Fatalf("precheck called MarshalJSON %d times; want 0", n)
	}
}

func TestEmbeddedFieldAmbiguityEncodeSelection(t *testing.T) {
	h := &JsonHandle{}

	// default (non-strict) behavior: the candidate seen first in
	// declaration order wins, matching the first path reported by the precheck.
	var out []byte
	v := tEmbAmbOuter{}
	v.tEmbAmbMidA.tEmbAmbInner.Name = "first"
	v.tEmbAmbMidB.tEmbAmbInner.Name = "second"
	if err := NewEncoderBytes(&out, h).Encode(v); err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	if s := string(out); s != `{"Name":"first"}` {
		t.Fatalf("encode selection: got %s, want %s", s, `{"Name":"first"}`)
	}

	// shallower (direct) field shadows the promoted one
	out = out[:0]
	v2 := tEmbShadowOuter{Name: "direct"}
	v2.tEmbAmbInner.Name = "promoted"
	if err := NewEncoderBytes(&out, h).Encode(v2); err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	if s := string(out); s != `{"Name":"direct"}` {
		t.Fatalf("encode shadowing: got %s, want %s", s, `{"Name":"direct"}`)
	}

	// non-strict encode of an ambiguous struct calls the marshaler of the
	// selected (first) candidate exactly once
	out = out[:0]
	var n int
	v3 := tEmbMarshOuter{}
	v3.tEmbMarshA.M.n = &n
	v3.tEmbMarshB.M.n = &n
	if err := NewEncoderBytes(&out, h).Encode(v3); err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("MarshalJSON called %d times; want 1", n)
	}
	if s := string(out); s != `{"M":"cm"}` {
		t.Fatalf("encode marshaler: got %s, want %s", s, `{"M":"cm"}`)
	}
}

func TestEmbeddedFieldAmbiguityStrictMode(t *testing.T) {
	for _, h := range []Handle{&JsonHandle{}, &CborHandle{}} {
		h.getBasicHandle().CheckEmbeddedFieldAmbiguity = true

		// strict mode rejects ambiguous structs before encoding them,
		// with zero extension (Marshaler) calls and zero bytes written.
		var out []byte
		var n int
		v := tEmbMarshOuter{}
		v.tEmbMarshA.M.n = &n
		v.tEmbMarshB.M.n = &n
		err := NewEncoderBytes(&out, h).Encode(v)
		if err == nil {
			t.Fatalf("%s: strict encode of ambiguous struct: want error, got nil (out=%q)",
				h.Name(), out)
		}
		var ambErr *StructFieldAmbiguityError
		if !errors.As(err, &ambErr) {
			t.Fatalf("%s: error %T (%v) is not a *StructFieldAmbiguityError", h.Name(), err, err)
		}
		if len(ambErr.Conflicts) != 1 || ambErr.Conflicts[0].Name != "M" ||
			!reflect.DeepEqual(ambErr.Conflicts[0].Paths, []string{"tEmbMarshA.M", "tEmbMarshB.M"}) {
			t.Fatalf("%s: unexpected conflicts: %+v", h.Name(), ambErr.Conflicts)
		}
		if n != 0 {
			t.Fatalf("%s: MarshalJSON called %d times on rejected encode; want 0", h.Name(), n)
		}
		if len(out) != 0 {
			t.Fatalf("%s: %d bytes written on rejected encode; want 0", h.Name(), len(out))
		}
		if !strings.Contains(err.Error(), `field name "M"`) {
			t.Fatalf("%s: error message missing conflict details: %v", h.Name(), err)
		}

		// strict mode still encodes unambiguous structs (and calls their marshaler)
		out = out[:0]
		v2 := tEmbShadowOuter{Name: "direct"}
		if err = NewEncoderBytes(&out, h).Encode(v2); err != nil {
			t.Fatalf("%s: strict encode of unambiguous struct failed: %v", h.Name(), err)
		}
	}
}
