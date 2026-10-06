// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// This file implements a shared map reference encoding mode ("mapref").
//
// The mapref mode encodes a struct whose map fields may share identity:
// two fields pointing at the same registered map are encoded as one
// definition plus one reference, and decode back into fields that point
// at the same map object.
//
// Only maps registered in a MapRefRegistry (under a stable definition id)
// are supported, and the input must be acyclic. The mode does not handle
// arbitrary structs, slices or self-cycles: non-map fields are encoded
// as plain trees, and a self-referencing map content is rejected by the
// underlying tree encoder. The regular tree codec is unchanged; mapref
// is a self-contained additive mode.
//
// Wire format (JSON):
//
//	{"Left": {"$def": "id-1", "$val": {...}}, "Right": {"$ref": "id-1"}}
//
// The first occurrence of a shared map is a definition carrying the
// stable id and the map content; later occurrences of the same map are
// references carrying the id. The id lives only in the envelope, so the
// original input maps are never written with ids. A stream that defines
// the same id twice, or references an id that was never defined, is
// rejected.

const (
	mapRefDefKey = "$def"
	mapRefRefKey = "$ref"
	mapRefValKey = "$val"
)

// MapRefRegistry registers the maps that may be shared during mapref
// encoding, each under a stable definition id.
type MapRefRegistry struct {
	byID  map[string]uintptr // definition id -> map identity
	byPtr map[uintptr]string // map identity -> definition id
}

// NewMapRefRegistry returns an empty MapRefRegistry.
func NewMapRefRegistry() *MapRefRegistry {
	return &MapRefRegistry{
		byID:  make(map[string]uintptr),
		byPtr: make(map[uintptr]string),
	}
}

// Register registers the non-nil map m under the stable definition id.
// It fails if id is empty or already registered, if m is not a map, or
// if m was already registered under a different id.
func (r *MapRefRegistry) Register(id string, m interface{}) error {
	if id == "" {
		return errors.New("mapref: register: empty definition id")
	}
	rv := reflect.ValueOf(m)
	if rv.Kind() != reflect.Map {
		return fmt.Errorf("mapref: register: %T is not a map", m)
	}
	if rv.IsNil() {
		return fmt.Errorf("mapref: register: nil map for id %q", id)
	}
	ptr := rv.Pointer()
	if _, ok := r.byID[id]; ok {
		return fmt.Errorf("mapref: register: duplicate definition id %q", id)
	}
	if prev, ok := r.byPtr[ptr]; ok {
		return fmt.Errorf("mapref: register: map already registered as %q", prev)
	}
	r.byID[id] = ptr
	r.byPtr[ptr] = id
	return nil
}

// idOf returns the definition id for the map identity, if registered.
func (r *MapRefRegistry) idOf(ptr uintptr) (string, bool) {
	id, ok := r.byPtr[ptr]
	return id, ok
}

// MapRefEncoder encodes a struct in mapref mode. A single encoder must
// not be used concurrently.
type MapRefEncoder struct {
	reg     *MapRefRegistry
	emitted map[uintptr]string // map identity -> definition id already emitted
}

// NewMapRefEncoder returns a MapRefEncoder using reg to resolve the
// stable definition ids of shared maps.
func NewMapRefEncoder(reg *MapRefRegistry) *MapRefEncoder {
	if reg == nil {
		reg = NewMapRefRegistry()
	}
	return &MapRefEncoder{reg: reg}
}

// Encode encodes v, which must be a struct or a pointer to a struct.
//
// Each non-nil map field must be registered in the encoder registry:
// its first occurrence is emitted as a definition, later occurrences of
// the same map as references. Nil map fields and non-map fields are
// emitted as plain tree values. The input is never mutated; ids appear
// only in the envelope.
func (e *MapRefEncoder) Encode(v interface{}) ([]byte, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil, errors.New("mapref: encode: nil pointer")
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("mapref: encode: expected struct, got %s", rv.Kind())
	}
	e.emitted = make(map[uintptr]string)
	rt := rv.Type()
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		name := mapRefFieldName(&sf)
		if name == "-" {
			continue
		}
		fv := rv.Field(i)
		var raw []byte
		var err error
		if fv.Kind() == reflect.Map && !fv.IsNil() {
			raw, err = e.encodeMapField(&sf, fv)
		} else {
			raw, err = json.Marshal(fv.Interface())
		}
		if err != nil {
			return nil, err
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		nameBytes, _ := json.Marshal(name)
		buf.Write(nameBytes)
		buf.WriteByte(':')
		buf.Write(raw)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// encodeMapField encodes one non-nil map field as a definition (first
// occurrence of the map) or a reference (subsequent occurrences).
func (e *MapRefEncoder) encodeMapField(sf *reflect.StructField, fv reflect.Value) ([]byte, error) {
	ptr := fv.Pointer()
	id, ok := e.reg.idOf(ptr)
	if !ok {
		return nil, fmt.Errorf("mapref: encode: field %s: map is not registered", sf.Name)
	}
	if prev, seen := e.emitted[ptr]; seen {
		return mapRefRefBytes(prev), nil
	}
	e.emitted[ptr] = id
	content, err := json.Marshal(fv.Interface())
	if err != nil {
		return nil, fmt.Errorf("mapref: encode: field %s: %w", sf.Name, err)
	}
	return mapRefDefBytes(id, content), nil
}

// MapRefDecoder decodes a mapref stream. Definitions seen during a
// Decode call are remembered so that references to them resolve to the
// same map object.
type MapRefDecoder struct {
	defs map[string]reflect.Value // definition id -> decoded map
}

// NewMapRefDecoder returns a MapRefDecoder.
func NewMapRefDecoder() *MapRefDecoder {
	return &MapRefDecoder{}
}

// Decode decodes a mapref stream into v, which must be a pointer to a
// struct. Map fields sharing a definition id are set to the same map
// object. A duplicate definition id or a reference to an undefined id
// is an error.
func (d *MapRefDecoder) Decode(data []byte, v interface{}) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return errors.New("mapref: decode: expected non-nil pointer to struct")
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return fmt.Errorf("mapref: decode: expected pointer to struct, got pointer to %s", rv.Kind())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("mapref: decode: %w", err)
	}
	d.defs = make(map[string]reflect.Value)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		name := mapRefFieldName(&sf)
		if name == "-" {
			continue
		}
		raw, ok := fields[name]
		if !ok {
			continue
		}
		fv := rv.Field(i)
		if !fv.CanSet() {
			continue
		}
		if err := d.decodeField(&sf, fv, raw); err != nil {
			return err
		}
	}
	return nil
}

// decodeField decodes one field, resolving a definition or reference
// envelope when the field is a map.
func (d *MapRefDecoder) decodeField(sf *reflect.StructField, fv reflect.Value, raw json.RawMessage) error {
	if fv.Kind() == reflect.Map {
		if isRef, id, val, ok, err := mapRefParseEnvelope(raw); err != nil {
			return fmt.Errorf("mapref: decode: field %s: %w", sf.Name, err)
		} else if ok {
			if isRef {
				return d.applyRef(sf, fv, id)
			}
			return d.applyDef(sf, fv, id, val)
		}
	}
	if err := json.Unmarshal(raw, fv.Addr().Interface()); err != nil {
		return fmt.Errorf("mapref: decode: field %s: %w", sf.Name, err)
	}
	return nil
}

// applyDef records a new definition and sets the field to a fresh map
// decoded from the definition content.
func (d *MapRefDecoder) applyDef(sf *reflect.StructField, fv reflect.Value, id string, val json.RawMessage) error {
	if _, dup := d.defs[id]; dup {
		return fmt.Errorf("mapref: decode: field %s: duplicate definition id %q", sf.Name, id)
	}
	mv := reflect.New(fv.Type())
	if err := json.Unmarshal(val, mv.Interface()); err != nil {
		return fmt.Errorf("mapref: decode: field %s: definition %q: %w", sf.Name, id, err)
	}
	d.defs[id] = mv.Elem()
	fv.Set(mv.Elem())
	return nil
}

// applyRef sets the field to the same map object previously decoded for
// the referenced definition id.
func (d *MapRefDecoder) applyRef(sf *reflect.StructField, fv reflect.Value, id string) error {
	mv, ok := d.defs[id]
	if !ok {
		return fmt.Errorf("mapref: decode: field %s: unknown reference id %q", sf.Name, id)
	}
	if !mv.Type().AssignableTo(fv.Type()) {
		return fmt.Errorf("mapref: decode: field %s: reference %q has type %s, not assignable to %s",
			sf.Name, id, mv.Type(), fv.Type())
	}
	fv.Set(mv)
	return nil
}

// mapRefFieldName returns the wire name of a struct field, honoring a
// json tag if present.
func mapRefFieldName(sf *reflect.StructField) string {
	name := sf.Name
	if tag, ok := sf.Tag.Lookup("json"); ok {
		if i := strings.IndexByte(tag, ','); i >= 0 {
			tag = tag[:i]
		}
		if tag == "-" {
			return "-"
		}
		if tag != "" {
			name = tag
		}
	}
	return name
}

// mapRefDefBytes builds a definition envelope from a stable id and the
// already-encoded map content.
func mapRefDefBytes(id string, val []byte) []byte {
	idBytes, _ := json.Marshal(id)
	var buf bytes.Buffer
	buf.WriteString(`{"$def":`)
	buf.Write(idBytes)
	buf.WriteString(`,"$val":`)
	buf.Write(val)
	buf.WriteByte('}')
	return buf.Bytes()
}

// mapRefRefBytes builds a reference envelope for a stable id.
func mapRefRefBytes(id string) []byte {
	idBytes, _ := json.Marshal(id)
	var buf bytes.Buffer
	buf.WriteString(`{"$ref":`)
	buf.Write(idBytes)
	buf.WriteByte('}')
	return buf.Bytes()
}

// mapRefParseEnvelope inspects a raw field value. If it is a definition
// or reference envelope, it returns whether it is a reference, the id,
// and (for definitions) the raw content. A plain value reports ok=false.
func mapRefParseEnvelope(raw json.RawMessage) (isRef bool, id string, val json.RawMessage, ok bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false, "", nil, false, nil
	}
	var probe map[string]json.RawMessage
	if err = json.Unmarshal(trimmed, &probe); err != nil {
		return false, "", nil, false, err
	}
	defRaw, hasDef := probe[mapRefDefKey]
	refRaw, hasRef := probe[mapRefRefKey]
	if !hasDef && !hasRef {
		return false, "", nil, false, nil
	}
	if hasDef == hasRef {
		return false, "", nil, false,
			fmt.Errorf("malformed envelope: exactly one of %s and %s is required", mapRefDefKey, mapRefRefKey)
	}
	if hasRef {
		if err = json.Unmarshal(refRaw, &id); err != nil || id == "" {
			return false, "", nil, false, errors.New("malformed envelope: bad reference id")
		}
		return true, id, nil, true, nil
	}
	if err = json.Unmarshal(defRaw, &id); err != nil || id == "" {
		return false, "", nil, false, errors.New("malformed envelope: bad definition id")
	}
	val, ok = probe[mapRefValKey]
	if !ok {
		return false, "", nil, false,
			fmt.Errorf("malformed envelope: definition %q is missing %s", id, mapRefValKey)
	}
	return false, id, val, true, nil
}
