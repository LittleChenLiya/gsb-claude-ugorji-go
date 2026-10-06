// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// Two small named types, each with its own BytesExt and a distinct wire form.
type snapSmallA uint8

type snapSmallB uint8

type snapSmallAExt struct{}
type snapSmallBExt struct{}

var (
	snapSmallATyp = reflect.TypeOf(snapSmallA(0))
	snapSmallBTyp = reflect.TypeOf(snapSmallB(0))
)

func (snapSmallAExt) WriteExt(v interface{}) []byte {
	a := v.(snapSmallA)
	return []byte{'A', byte(a)}
}

func (snapSmallAExt) ReadExt(dst interface{}, src []byte) {
	p := dst.(*snapSmallA)
	if len(src) != 2 || src[0] != 'A' {
		halt.onerror(errors.New("bad snapSmallA payload"))
		return
	}
	*p = snapSmallA(src[1])
}

func (snapSmallBExt) WriteExt(v interface{}) []byte {
	b := v.(snapSmallB)
	return []byte{'B', byte(b)}
}

func (snapSmallBExt) ReadExt(dst interface{}, src []byte) {
	p := dst.(*snapSmallB)
	if len(src) != 2 || src[0] != 'B' {
		halt.onerror(errors.New("bad snapSmallB payload"))
		return
	}
	*p = snapSmallB(src[1])
}

func TestExtTagConflictRejected(t *testing.T) {
	h := new(MsgpackHandle)

	// tag 5 -> snapSmallA
	if err := h.SetBytesExt(snapSmallATyp, 5, snapSmallAExt{}); err != nil {
		t.Fatalf("initial SetBytesExt: %v", err)
	}
	// same tag, different real type -> rejected
	err := h.SetBytesExt(snapSmallBTyp, 5, snapSmallBExt{})
	if !errors.Is(err, errExtTagConflict) {
		t.Fatalf("expected errExtTagConflict, got: %v", err)
	}

	// no half Handle state: tag 5 still resolves to snapSmallA,
	// and snapSmallB is not registered at all.
	bh := h.getBasicHandle()
	fn := bh.getExtForTag(5)
	if fn == nil || fn.rtid != rt2id(snapSmallATyp) {
		t.Fatalf("tag 5 must still map to snapSmallA, got: %#v", fn)
	}
	if fn2 := bh.getExt(rt2id(snapSmallBTyp), true); fn2 != nil {
		t.Fatalf("snapSmallB must not be registered after conflict, got: %#v", fn2)
	}

	// same (type, tag) pair is still allowed to be updated.
	if err := h.SetBytesExt(snapSmallATyp, 5, snapSmallAExt{}); err != nil {
		t.Fatalf("re-setting same type/tag: %v", err)
	}

	// deregistering snapSmallA frees the tag, which can then be reused.
	if err := h.SetExt(snapSmallATyp, 5, nil); err != nil {
		t.Fatalf("deregister snapSmallA: %v", err)
	}
	if fn := bh.getExtForTag(5); fn != nil {
		t.Fatalf("tag 5 should be gone after deregistration, got: %#v", fn)
	}
	if err := h.SetBytesExt(snapSmallBTyp, 5, snapSmallBExt{}); err != nil {
		t.Fatalf("reuse freed tag: %v", err)
	}
}

func TestSnapshotIsolatedFromLaterRegistration(t *testing.T) {
	h := new(MsgpackHandle)
	if err := h.SetBytesExt(snapSmallATyp, 5, snapSmallAExt{}); err != nil {
		t.Fatalf("register snapSmallA: %v", err)
	}

	// Snapshot before snapSmallB exists.
	snap := Snapshot(h)

	// Original entry point still works on the live (still-uninitialized) Handle.
	if err := h.SetBytesExt(snapSmallBTyp, 6, snapSmallBExt{}); err != nil {
		t.Fatalf("register snapSmallB on live handle: %v", err)
	}

	// And the same tag conflict is enforced against the live handle's new state.
	if err := h.SetBytesExt(snapSmallATyp, 6, snapSmallAExt{}); !errors.Is(err, errExtTagConflict) {
		t.Fatalf("expected tag conflict on live handle, got: %v", err)
	}

	// The snapshot is immutable: registration on it fails.
	if err := snap.SetBytesExt(snapSmallBTyp, 6, snapSmallBExt{}); !errors.Is(err, errHandleInited) {
		t.Fatalf("snapshot must reject new registrations, got: %v", err)
	}

	aOrig := snapSmallA(7)

	// Encode via the snapshot, after the live handle was already reconfigured.
	var bs []byte
	enc := NewEncoderBytes(&bs, snap)
	if err := enc.Encode(aOrig); err != nil {
		t.Fatalf("snapshot encode: %v", err)
	}

	// Live handle cannot encode snapSmallA? It still can (snapSmallA stays
	// registered); but the snapshot must be unaffected either way.
	// Decode through the snapshot round-trips to the original type.
	var aBack snapSmallA
	dec := NewDecoderBytes(bs, snap)
	if err := dec.Decode(&aBack); err != nil {
		t.Fatalf("snapshot decode: %v", err)
	}
	if aBack != aOrig {
		t.Fatalf("snapshot round-trip mismatch: got %d, want %d", aBack, aOrig)
	}

	// Sanity: payload carries snapSmallA's marker, and the snapshot never
	// learned about snapSmallB.
	if !bytes.Contains(bs, []byte{'A', 7}) {
		t.Fatalf("encoded payload not produced by snapSmallAExt: % x", bs)
	}
	var b snapSmallB
	if err := NewDecoderBytes(bs, snap).Decode(&b); err == nil {
		t.Fatalf("snapshot must not decode snapSmallA bytes as snapSmallB")
	}

	// A fresh encoder on the live handle sees both extensions and round-trips
	// snapSmallB, proving the snapshot was not mutated by the live handle.
	var bsB []byte
	if err := NewEncoderBytes(&bsB, h).Encode(snapSmallB(9)); err != nil {
		t.Fatalf("live encode snapSmallB: %v", err)
	}
	if !bytes.Contains(bsB, []byte{'B', 9}) {
		t.Fatalf("live payload not produced by snapSmallBExt: % x", bsB)
	}
	var bBack snapSmallB
	if err := NewDecoderBytes(bsB, h).Decode(&bBack); err != nil {
		t.Fatalf("live decode snapSmallB: %v", err)
	}
	if bBack != 9 {
		t.Fatalf("live round-trip mismatch: got %d", bBack)
	}
}

// TestSnapshotRoundTripsAfterLiveReTag simulates an old snapshot vs a live
// handle that later maps the same tag space to another small type.
func TestSnapshotRoundTripsAfterLiveTagRebind(t *testing.T) {
	h := new(MsgpackHandle)
	if err := h.SetBytesExt(snapSmallATyp, 5, snapSmallAExt{}); err != nil {
		t.Fatalf("register snapSmallA: %v", err)
	}
	snap := Snapshot(h)

	aOrig := snapSmallA(3)
	var bs []byte
	if err := NewEncoderBytes(&bs, snap).Encode(aOrig); err != nil {
		t.Fatalf("snapshot encode: %v", err)
	}

	// Free tag 5 on the live handle and bind it to snapSmallB instead.
	if err := h.SetExt(snapSmallATyp, 5, nil); err != nil {
		t.Fatalf("deregister snapSmallA: %v", err)
	}
	if err := h.SetBytesExt(snapSmallBTyp, 5, snapSmallBExt{}); err != nil {
		t.Fatalf("re-bind tag 5 to snapSmallB: %v", err)
	}

	// Old snapshot still decodes tag 5 as snapSmallA.
	var aBack snapSmallA
	if err := NewDecoderBytes(bs, snap).Decode(&aBack); err != nil {
		t.Fatalf("old snapshot decode after rebind: %v", err)
	}
	if aBack != aOrig {
		t.Fatalf("old snapshot type changed: got %d, want %d", aBack, aOrig)
	}

	// And bytes encoded through the old snapshot cannot be misread by the
	// live handle as snapSmallB's wire format.
	var bBack snapSmallB
	if err := NewDecoderBytes(bytes.Clone(bs), h).Decode(&bBack); err == nil {
		t.Fatalf("live handle should not read snapSmallA ext payload as snapSmallB")
	}
}

// InterfaceExt view of the two small types, for codecs like cbor.
type snapSmallAIntfExt struct{}
type snapSmallBIntfExt struct{}

func (snapSmallAIntfExt) ConvertExt(v interface{}) interface{} {
	return []interface{}{"A", uint64(v.(snapSmallA))}
}

func (snapSmallAIntfExt) UpdateExt(dst interface{}, src interface{}) {
	m := src.([]interface{})
	*dst.(*snapSmallA) = snapSmallA(m[1].(uint64))
}

func (snapSmallBIntfExt) ConvertExt(v interface{}) interface{} {
	return []interface{}{"B", uint64(v.(snapSmallB))}
}

func (snapSmallBIntfExt) UpdateExt(dst interface{}, src interface{}) {
	m := src.([]interface{})
	*dst.(*snapSmallB) = snapSmallB(m[1].(uint64))
}

// The deprecated AddExt entry point enforces the same tag conflict rule.
func TestExtTagConflictViaAddExt(t *testing.T) {
	h := new(MsgpackHandle)
	enc := func(rv reflect.Value) ([]byte, error) { return snapSmallAExt{}.WriteExt(rv2i(rv)), nil }
	dec := func(rv reflect.Value, bs []byte) error { snapSmallAExt{}.ReadExt(rv2i(rv), bs); return nil }
	encB := func(rv reflect.Value) ([]byte, error) { return snapSmallBExt{}.WriteExt(rv2i(rv)), nil }
	decB := func(rv reflect.Value, bs []byte) error { snapSmallBExt{}.ReadExt(rv2i(rv), bs); return nil }

	if err := h.AddExt(snapSmallATyp, 20, enc, dec); err != nil {
		t.Fatalf("AddExt snapSmallA: %v", err)
	}
	if err := h.AddExt(snapSmallBTyp, 20, encB, decB); !errors.Is(err, errExtTagConflict) {
		t.Fatalf("expected conflict via AddExt, got: %v", err)
	}
	if fn := h.getBasicHandle().getExt(rt2id(snapSmallBTyp), true); fn != nil {
		t.Fatalf("snapSmallB must not be half-registered, got: %#v", fn)
	}
}

// Snapshot isolation works for InterfaceExt codecs too (cbor), using both
// small types.
func TestSnapshotCborInterfaceExt(t *testing.T) {
	h := new(CborHandle)
	if err := h.SetInterfaceExt(snapSmallATyp, 40, snapSmallAIntfExt{}); err != nil {
		t.Fatalf("register snapSmallA: %v", err)
	}
	snap := Snapshot(h)
	if err := h.SetInterfaceExt(snapSmallBTyp, 41, snapSmallBIntfExt{}); err != nil {
		t.Fatalf("register snapSmallB on live: %v", err)
	}
	// Snapshot never knew about tag 41.
	if err := snap.SetInterfaceExt(snapSmallBTyp, 41, snapSmallBIntfExt{}); !errors.Is(err, errHandleInited) {
		t.Fatalf("snapshot must reject registration, got: %v", err)
	}

	orig := snapSmallA(42)
	var bs []byte
	if err := NewEncoderBytes(&bs, snap).Encode(orig); err != nil {
		t.Fatalf("snapshot cbor encode: %v", err)
	}
	var back snapSmallA
	if err := NewDecoderBytes(bs, snap).Decode(&back); err != nil {
		t.Fatalf("snapshot cbor decode: %v", err)
	}
	if back != orig {
		t.Fatalf("snapshot round-trip mismatch: got %d, want %d", back, orig)
	}
}

// A snapshot can be taken from a Handle that is already in use, and a
// snapshot can itself be snapshotted; in both cases it stays functional and
// immutable.
func TestSnapshotFromInitializedHandle(t *testing.T) {
	h := new(MsgpackHandle)
	if err := h.SetBytesExt(snapSmallATyp, 5, snapSmallAExt{}); err != nil {
		t.Fatalf("register snapSmallA: %v", err)
	}
	// initialize h through use
	var bs []byte
	if err := NewEncoderBytes(&bs, h).Encode(snapSmallA(1)); err != nil {
		t.Fatalf("use live handle: %v", err)
	}

	snap1 := Snapshot(h)
	snap2 := Snapshot(snap1)

	for i, s := range []*MsgpackHandle{snap1, snap2} {
		var out []byte
		if err := NewEncoderBytes(&out, s).Encode(snapSmallA(11 + i)); err != nil {
			t.Fatalf("snapshot %d encode: %v", i, err)
		}
		var back snapSmallA
		if err := NewDecoderBytes(out, s).Decode(&back); err != nil {
			t.Fatalf("snapshot %d decode: %v", i, err)
		}
		if back != snapSmallA(11+i) {
			t.Fatalf("snapshot %d round-trip mismatch: %d", i, back)
		}
		if err := s.SetBytesExt(snapSmallBTyp, 9, snapSmallBExt{}); !errors.Is(err, errHandleInited) {
			t.Fatalf("snapshot %d must be immutable, got: %v", i, err)
		}
	}
}
