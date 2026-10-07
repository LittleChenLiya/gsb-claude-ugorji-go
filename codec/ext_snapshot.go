// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
)

// SnapshotHandle returns an immutable snapshot of the given Handle, bound to
// the handle's current extension registry (and registered interfaces).
//
// The snapshot shares the concrete handle type (e.g. *MsgpackHandle) and its
// configured options, but owns a deep copy of the extension registry.
// Encoding and decoding through the snapshot are bound to that copied
// registry, so later registrations (or de-registrations) on the original
// handle cannot affect the snapshot, and vice versa.
//
// The snapshot is initialized before it is returned, making it immutable:
// calls to SetExt/AddExt (or the format-specific SetBytesExt/SetInterfaceExt)
// on it will return an error (errHandleInited).
//
// The original handle is not modified by SnapshotHandle, and its registration
// entry points remain usable as before.
//
// An error is returned if h is nil, or is not a pointer to a struct handle.
// All the built-in handles (JsonHandle, CborHandle, MsgpackHandle,
// BincHandle, SimpleHandle, ...) qualify.
func SnapshotHandle(h Handle) (Handle, error) {
	if h == nil {
		return nil, errors.New("codec.SnapshotHandle: nil Handle")
	}
	rv := reflect.ValueOf(h)
	if rv.Kind() != reflect.Ptr || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("codec.SnapshotHandle: expects a pointer to a struct Handle, got %T", h)
	}

	// shallow-copy the concrete handle, preserving its type and options.
	nv := reflect.New(rv.Elem().Type())
	nv.Elem().Set(rv.Elem())
	nh, ok := nv.Interface().(Handle)
	if !ok {
		return nil, fmt.Errorf("codec.SnapshotHandle: copied value of type %v does not implement Handle", nv.Type())
	}

	// Rebind the runtime state of the copy:
	//   - deep-copy the extension registry and registered interfaces, so
	//     later mutations of the source handle cannot leak into the snapshot.
	//   - reset derived runtime state (fn caches, pools, locks), so it is
	//     rebuilt against the snapshot and its own registry.
	src := h.getBasicHandle()
	dst := nh.getBasicHandle()
	dst.extHandle = src.extHandle.clone()
	dst.intf2impls = src.intf2impls.clone()
	dst.rtidFnsEncIO = atomicRtidFnSlice{}
	dst.rtidFnsEncNoExtIO = atomicRtidFnSlice{}
	dst.rtidFnsEncBytes = atomicRtidFnSlice{}
	dst.rtidFnsEncNoExtBytes = atomicRtidFnSlice{}
	dst.rtidFnsDecIO = atomicRtidFnSlice{}
	dst.rtidFnsDecNoExtIO = atomicRtidFnSlice{}
	dst.rtidFnsDecBytes = atomicRtidFnSlice{}
	dst.rtidFnsDecNoExtBytes = atomicRtidFnSlice{}
	dst.sideEncPool = sync.Pool{}
	dst.sideDecPool = sync.Pool{}
	dst.mu = sync.Mutex{}

	// Initialize the snapshot itself. This derives the handle flags from the
	// snapshot, binds its pools to the snapshot (not the source handle), and
	// marks it initialized, which makes the snapshot immutable to further
	// registration. The source handle is left untouched.
	atomic.StoreUint32(&dst.inited, 0)
	initHandle(nh)
	return nh, nil
}

// clone returns a deep copy of the extension registry, so that mutations
// (appends or in-place updates) of one copy never affect the other.
func (o extHandle) clone() extHandle {
	if o == nil {
		return nil
	}
	c := make(extHandle, len(o))
	copy(c, o)
	return c
}

// clone returns a deep copy of the registered interface mappings.
func (o intf2impls) clone() intf2impls {
	if o == nil {
		return nil
	}
	c := make(intf2impls, len(o))
	copy(c, o)
	return c
}
