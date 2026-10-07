// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

// ---- small fixture types and their extensions ----

type extSnapAlpha string
type extSnapBeta int32

var (
	extSnapAlphaTyp = reflect.TypeOf(extSnapAlpha(""))
	extSnapBetaTyp  = reflect.TypeOf(extSnapBeta(0))
)

type extSnapAlphaExt struct{}

func (extSnapAlphaExt) WriteExt(v interface{}) []byte {
	return []byte(string(v.(extSnapAlpha)))
}

func (extSnapAlphaExt) ReadExt(dst interface{}, src []byte) {
	*(dst.(*extSnapAlpha)) = extSnapAlpha(src)
}

type extSnapBetaExt struct{}

func (extSnapBetaExt) WriteExt(v interface{}) []byte {
	return []byte(fmt.Sprintf("%d", v.(extSnapBeta)))
}

func (extSnapBetaExt) ReadExt(dst interface{}, src []byte) {
	var n int
	fmt.Sscanf(string(src), "%d", &n)
	*(dst.(*extSnapBeta)) = extSnapBeta(n)
}

func newExtSnapHandle() *MsgpackHandle {
	var h MsgpackHandle
	h.WriteExt = true // honor extension tags on encode
	return &h
}

// ---- tests ----

// A registration whose tag is already bound to a different type must be
// rejected, and must leave the registry unchanged (no half-registered state).
func TestExtSnapshotTagConflictRejected(t *testing.T) {
	h := newExtSnapHandle()
	if err := h.SetExt(extSnapAlphaTyp, 7, makeExt(extSnapAlphaExt{})); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	lenBefore := len(h.extHandle)

	err := h.SetExt(extSnapBetaTyp, 7, makeExt(extSnapBetaExt{}))
	if err == nil {
		t.Fatal("expected conflict error registering beta on alpha's tag")
	}

	// no half state: registry is exactly as before the rejected call.
	if len(h.extHandle) != lenBefore {
		t.Fatalf("registry mutated by rejected registration: len %d -> %d", lenBefore, len(h.extHandle))
	}
	if v := h.extHandle.getExtForTag(7); v == nil || v.rtid != rt2id(extSnapAlphaTyp) {
		t.Fatal("tag 7 no longer bound to alpha after rejected registration")
	}
	if v := h.extHandle.getExt(rt2id(extSnapBetaTyp), true); v != nil {
		t.Fatal("beta partially registered after rejected registration")
	}

	// re-registering the same type on its own tag is not a conflict.
	if err = h.SetExt(extSnapAlphaTyp, 7, makeExt(extSnapAlphaExt{})); err != nil {
		t.Fatalf("re-register alpha on its own tag: %v", err)
	}
	// the other type can still register on a free tag.
	if err = h.SetExt(extSnapBetaTyp, 8, makeExt(extSnapBetaExt{})); err != nil {
		t.Fatalf("register beta on free tag: %v", err)
	}
}

// A snapshot is immutable, and stays bound to its own registry copy:
// later registrations on the original handle must not affect it, and the
// original registration entry points remain usable.
func TestExtSnapshotImmutableAndIsolated(t *testing.T) {
	h := newExtSnapHandle()
	if err := h.SetExt(extSnapAlphaTyp, 7, makeExt(extSnapAlphaExt{})); err != nil {
		t.Fatalf("register alpha: %v", err)
	}

	snap, err := SnapshotHandle(h)
	if err != nil {
		t.Fatalf("SnapshotHandle: %v", err)
	}
	if _, ok := snap.(*MsgpackHandle); !ok {
		t.Fatalf("snapshot has type %T, want *MsgpackHandle", snap)
	}

	// the snapshot is immutable: registration on it fails.
	if err = snap.(*MsgpackHandle).SetExt(extSnapBetaTyp, 8, makeExt(extSnapBetaExt{})); err == nil {
		t.Fatal("expected error registering on immutable snapshot")
	}

	// encode alpha through the snapshot before any further registration.
	var bs1 []byte
	NewEncoderBytes(&bs1, snap).MustEncode(extSnapAlpha("hello"))

	// the original registration entry still works after the snapshot.
	if err = h.SetExt(extSnapBetaTyp, 8, makeExt(extSnapBetaExt{})); err != nil {
		t.Fatalf("register beta on original handle: %v", err)
	}

	// the old snapshot still round-trips the original type.
	var a extSnapAlpha
	NewDecoderBytes(bs1, snap).MustDecode(&a)
	if a != "hello" {
		t.Fatalf("snapshot round-trip: got %q, want %q", a, "hello")
	}

	// the snapshot's view of the registry is unchanged by the later
	// registration on the original handle.
	if n := len(snap.getBasicHandle().extHandle); n != 1 {
		t.Fatalf("snapshot registry grew after later registration: len %d", n)
	}
	if v := snap.getBasicHandle().extHandle.getExtForTag(8); v != nil {
		t.Fatal("snapshot sees tag registered later on the original handle")
	}

	// encoding through the snapshot is unchanged by the later registration.
	var bs2 []byte
	NewEncoderBytes(&bs2, snap).MustEncode(extSnapAlpha("hello"))
	if !bytes.Equal(bs1, bs2) {
		t.Fatalf("snapshot encoding changed after later registration: %v -> %v", bs1, bs2)
	}

	// the original handle uses its own (extended) registry.
	var b extSnapBeta
	var bsB []byte
	NewEncoderBytes(&bsB, h).MustEncode(extSnapBeta(42))
	NewDecoderBytes(bsB, h).MustDecode(&b)
	if b != 42 {
		t.Fatalf("original handle round-trip: got %d, want 42", b)
	}
}

func ExampleSnapshotHandle() {
	var h MsgpackHandle
	h.WriteExt = true
	if err := h.SetExt(extSnapAlphaTyp, 7, makeExt(extSnapAlphaExt{})); err != nil {
		panic(err)
	}

	// take an immutable snapshot bound to the current extension registry.
	snap, err := SnapshotHandle(&h)
	if err != nil {
		panic(err)
	}

	// the original handle can still register more extensions afterwards;
	// the snapshot is not affected by this.
	if err = h.SetExt(extSnapBetaTyp, 8, makeExt(extSnapBetaExt{})); err != nil {
		panic(err)
	}

	var bs []byte
	if err = NewEncoderBytes(&bs, snap).Encode(extSnapAlpha("hello")); err != nil {
		panic(err)
	}
	var v extSnapAlpha
	if err = NewDecoderBytes(bs, snap).Decode(&v); err != nil {
		panic(err)
	}
	fmt.Println(v)
	// Output: hello
}
