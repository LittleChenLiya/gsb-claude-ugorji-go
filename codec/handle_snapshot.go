// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"reflect"
	"sync"
)

// Snapshot returns an immutable, initialized snapshot of the Handle.
//
// The snapshot captures the Handle's configuration and registered extensions
// at the time it is taken. It is initialized immediately, so extensions cannot
// be added to or removed from it. Encoders and Decoders created from the
// snapshot are bound to the captured extension registrations: subsequent
// registrations on the original Handle do not affect the snapshot, and an
// extension already encoded from an old snapshot still decodes back to its
// original type even if the original Handle is later reconfigured.
//
// Taking a snapshot does not initialize or otherwise modify the original
// Handle, so the original registration entry points (AddExt, SetExt,
// SetBytesExt, SetInterfaceExt) remain usable on it as before.
//
// H is the concrete Handle type (e.g. *MsgpackHandle, *CborHandle).
func Snapshot[H Handle](h H) (snap H) {
	// h is a pointer to a concrete Handle struct embedding BasicHandle by value.
	v := reflect.ValueOf(h).Elem() // the concrete struct value
	bh := h.getBasicHandle()

	// Shallow-copy the whole concrete Handle, so format-specific options travel
	// along with the BasicHandle configuration.
	vc := reflect.New(v.Type()).Elem()
	vc.Set(v)

	snap = vc.Addr().Interface().(H)
	snapBH := snap.getBasicHandle()

	// Detach the mutable registration/caching slices: the snapshot owns private
	// copies, so later mutation of the original Handle cannot reach it and
	// lazily-built encode/decode caches stay local to the snapshot.
	if n := len(bh.extHandle); n > 0 {
		ext := make(extHandle, n)
		copy(ext, bh.extHandle)
		snapBH.extHandle = ext
	} else {
		snapBH.extHandle = nil
	}
	if n := len(bh.intf2impls); n > 0 {
		impls := make(intf2impls, n)
		copy(impls, bh.intf2impls)
		snapBH.intf2impls = impls
	} else {
		snapBH.intf2impls = nil
	}

	snapBH.rtidFnsEncIO = atomicRtidFnSlice{}
	snapBH.rtidFnsEncNoExtIO = atomicRtidFnSlice{}
	snapBH.rtidFnsEncBytes = atomicRtidFnSlice{}
	snapBH.rtidFnsEncNoExtBytes = atomicRtidFnSlice{}
	snapBH.rtidFnsDecIO = atomicRtidFnSlice{}
	snapBH.rtidFnsDecNoExtIO = atomicRtidFnSlice{}
	snapBH.rtidFnsDecBytes = atomicRtidFnSlice{}
	snapBH.rtidFnsDecNoExtBytes = atomicRtidFnSlice{}

	// sync.Pool must not be copied after first use; reinitialize them fresh.
	snapBH.sideEncPool = sync.Pool{}
	snapBH.sideDecPool = sync.Pool{}

	// Do not carry over lock state from the source Handle.
	snapBH.mu = sync.Mutex{}

	// Runtime-only flags are recomputed from the concrete Handle in initHandle2.
	snapBH.jsonHandle = false
	snapBH.binaryHandle = false
	snapBH.timeBuiltin = false

	// Force initialization even if the source Handle was already initialized
	// (e.g. taking a snapshot of another snapshot): the fresh pools above must
	// be wired up by initHandle2.
	snapBH.inited = 0

	// Recompute runtime-only flags and side-encoder pools, and mark inited,
	// exactly as a first-time use would.
	initHandle(snap)
	return
}
