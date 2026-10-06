// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

// Package mapref implements a directed encoding mode that preserves the
// identity of shared maps inside a small acyclic graph of map[string]any
// values.
//
// The mode is intentionally narrow:
//
//   - Only maps explicitly registered with a stable definition id (via
//     Register) are shared by reference. The first occurrence of a
//     registered map is emitted as a definition {"$def": id, "$val": {...}};
//     every later occurrence is emitted as a reference {"$ref": id}.
//   - Unregistered maps encode as ordinary inline trees, matching the
//     regular tree codec behavior (a shared unregistered map decodes into
//     distinct copies).
//   - Only JSON scalar values (nil, bool, string, numbers) and nested
//     map[string]any values are supported. Arbitrary structs, slices and
//     other types are rejected, as are cyclic inputs (including
//     self-references).
//   - The input graph is never mutated: definition/reference ids live only
//     in the encoded stream, not in the source maps.
//
// Decoding is stream-driven: a definition id may be defined at most once
// (a duplicate definition is an error) and every reference must resolve to
// a definition that precedes it in canonical stream order (object keys are
// visited sorted, depth-first - the same order Encode emits); an unknown
// reference is an error. All occurrences of a reference decode to the
// exact same map object as the corresponding definition.
//
// The keys "$def", "$ref" and "$val" are reserved and must not appear as
// regular keys in the input graph.
package mapref

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unsafe"
)

// Reserved stream keys.
const (
	keyDef = "$def"
	keyRef = "$ref"
	keyVal = "$val"
)

// Codec encodes and decodes acyclic graphs of maps, preserving the
// identity of registered maps across multiple occurrences.
//
// The zero value is not usable; construct one with New. A Codec is not
// safe for concurrent use.
type Codec struct {
	byID  map[string]map[string]any
	byMap map[uintptr]string
}

// New returns a Codec with an empty registry.
func New() *Codec {
	return &Codec{
		byID:  make(map[string]map[string]any),
		byMap: make(map[uintptr]string),
	}
}

// mapPtr returns the identity of a map value (its underlying header
// pointer), used to recognize the same map object across occurrences.
func mapPtr(m map[string]any) uintptr {
	return *(*uintptr)(unsafe.Pointer(&m))
}

// Register enrolls m under the stable definition id id, so that every
// occurrence of this exact map object in an encoded graph is shared by
// reference.
//
// Registering the same id twice, or the same map twice, is rejected as a
// duplicate definition.
func (c *Codec) Register(id string, m map[string]any) error {
	if id == "" {
		return errors.New("mapref: definition id must not be empty")
	}
	if m == nil {
		return fmt.Errorf("mapref: cannot register nil map for id %q", id)
	}
	if _, ok := c.byID[id]; ok {
		return fmt.Errorf("mapref: duplicate definition id %q", id)
	}
	p := mapPtr(m)
	if prev, ok := c.byMap[p]; ok {
		return fmt.Errorf("mapref: map already registered under id %q", prev)
	}
	c.byID[id] = m
	c.byMap[p] = id
	return nil
}

// Encode serializes the acyclic graph rooted at root. Registered maps are
// emitted once as a definition and thereafter as references; all other
// maps are emitted as ordinary inline trees. The input is not modified.
func (c *Codec) Encode(root map[string]any) ([]byte, error) {
	if root == nil {
		return nil, errors.New("mapref: cannot encode nil root")
	}
	e := &encoder{
		c:       c,
		defined: make(map[string]bool),
		active:  make(map[uintptr]bool),
	}
	v, err := e.encodeMap(root)
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// Decode parses data produced by Encode and returns the decoded graph.
// Every reference resolves to the same map object as its definition.
// Duplicate definitions and unknown references are rejected.
func (c *Codec) Decode(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("mapref: invalid stream: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("mapref: trailing data after root value")
	}
	d := &decoder{defs: make(map[string]map[string]any)}
	v, err := d.node(raw)
	if err != nil {
		return nil, err
	}
	root, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("mapref: root must be an object, got %T", raw)
	}
	return root, nil
}

type encoder struct {
	c       *Codec
	defined map[string]bool  // definition ids already emitted
	active  map[uintptr]bool // maps currently being encoded (cycle guard)
}

func (e *encoder) encodeMap(m map[string]any) (any, error) {
	p := mapPtr(m)
	if e.active[p] {
		return nil, errors.New("mapref: cyclic input is not supported")
	}
	if id, ok := e.c.byMap[p]; ok {
		if e.defined[id] {
			return map[string]any{keyRef: id}, nil
		}
		e.defined[id] = true
		body, err := e.inline(m, p)
		if err != nil {
			return nil, err
		}
		return map[string]any{keyDef: id, keyVal: body}, nil
	}
	return e.inline(m, p)
}

// inline encodes m as an ordinary JSON object. Keys are visited in sorted
// order so the emitted stream (and thus definition placement) is stable.
func (e *encoder) inline(m map[string]any, p uintptr) (any, error) {
	e.active[p] = true
	defer delete(e.active, p)

	keys := make([]string, 0, len(m))
	for k := range m {
		if k == keyDef || k == keyRef || k == keyVal {
			return nil, fmt.Errorf("mapref: key %q is reserved", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]any, len(m))
	for _, k := range keys {
		v, err := e.value(m[k])
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

func (e *encoder) value(v any) (any, error) {
	switch t := v.(type) {
	case nil, bool, string,
		float32, float64,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number:
		return v, nil
	case map[string]any:
		return e.encodeMap(t)
	default:
		return nil, fmt.Errorf("mapref: unsupported value type %T", v)
	}
}

type decoder struct {
	defs map[string]map[string]any // definition id -> decoded shared map
}

// node decodes a stream value, resolving definition and reference
// wrappers. Plain objects decode into fresh maps.
func (d *decoder) node(v any) (any, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		if _, isArr := v.([]any); isArr {
			return nil, errors.New("mapref: arrays are not supported")
		}
		return v, nil // JSON scalar
	}
	if rawID, ok := obj[keyDef]; ok {
		if len(obj) != 2 {
			return nil, fmt.Errorf("mapref: malformed definition for id %v", rawID)
		}
		id, ok := rawID.(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("mapref: definition id must be a non-empty string, got %v", rawID)
		}
		if _, ok := d.defs[id]; ok {
			return nil, fmt.Errorf("mapref: duplicate definition id %q", id)
		}
		body, ok := obj[keyVal].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mapref: definition %q must wrap an object", id)
		}
		m, err := d.plain(body)
		if err != nil {
			return nil, err
		}
		d.defs[id] = m
		return m, nil
	}
	if rawID, ok := obj[keyRef]; ok {
		if len(obj) != 1 {
			return nil, fmt.Errorf("mapref: malformed reference for id %v", rawID)
		}
		id, ok := rawID.(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("mapref: reference id must be a non-empty string, got %v", rawID)
		}
		m, ok := d.defs[id]
		if !ok {
			return nil, fmt.Errorf("mapref: unknown reference id %q", id)
		}
		return m, nil
	}
	return d.plain(obj)
}

// plain decodes a plain (non-wrapper) object into a fresh map. Keys are
// visited in sorted order, matching the encoder's canonical traversal, so
// a definition is always decoded before any reference to it.
func (d *decoder) plain(obj map[string]any) (map[string]any, error) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	m := make(map[string]any, len(obj))
	for _, k := range keys {
		dv, err := d.node(obj[k])
		if err != nil {
			return nil, err
		}
		m[k] = dv
	}
	return m, nil
}
