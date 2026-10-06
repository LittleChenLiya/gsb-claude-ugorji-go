// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package mapref_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ugorji/go/codec/mapref"
)

// sharedGraph builds the minimal directed fixture: two fields of the root
// point at the exact same "shared" map object.
func sharedGraph() (root, shared map[string]any) {
	shared = map[string]any{"name": "defaults", "retries": 3}
	root = map[string]any{
		"alpha": map[string]any{"cfg": shared},
		"beta":  map[string]any{"cfg": shared},
	}
	return root, shared
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", v)
	}
	return m
}

func TestSharedMapRoundTripPreservesIdentity(t *testing.T) {
	root, shared := sharedGraph()

	c := mapref.New()
	if err := c.Register("defaults/v1", shared); err != nil {
		t.Fatalf("register: %v", err)
	}
	data, err := c.Encode(root)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The stream must contain exactly one definition and one reference
	// carrying the stable id.
	if got := strings.Count(string(data), `"$def":"defaults/v1"`); got != 1 {
		t.Fatalf("expected 1 definition, got %d in %s", got, data)
	}
	if got := strings.Count(string(data), `"$ref":"defaults/v1"`); got != 1 {
		t.Fatalf("expected 1 reference, got %d in %s", got, data)
	}

	out, err := c.Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a := asMap(t, asMap(t, out["alpha"])["cfg"])
	b := asMap(t, asMap(t, out["beta"])["cfg"])

	// Identity: both fields must point at the same decoded map object.
	a["marker"] = "seen"
	if b["marker"] != "seen" {
		t.Fatal("shared map identity lost: fields decoded to distinct objects")
	}
	if a["name"] != "defaults" {
		t.Fatalf("shared map content corrupted: %v", a)
	}
}

func TestUnregisteredMapKeepsTreeSemantics(t *testing.T) {
	root, _ := sharedGraph() // nothing registered

	c := mapref.New()
	data, err := c.Encode(root)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(data), "$ref") || strings.Contains(string(data), "$def") {
		t.Fatalf("unregistered map must encode inline, got %s", data)
	}
	out, err := c.Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a := asMap(t, asMap(t, out["alpha"])["cfg"])
	b := asMap(t, asMap(t, out["beta"])["cfg"])
	a["marker"] = "seen"
	if _, ok := b["marker"]; ok {
		t.Fatal("unregistered map must decode into distinct copies")
	}
}

func TestDuplicateDefinitionRejected(t *testing.T) {
	c := mapref.New()

	// Duplicate registration of the same id.
	shared := map[string]any{"k": "v"}
	if err := c.Register("dup", shared); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := c.Register("dup", map[string]any{"k": "w"}); err == nil {
		t.Fatal("duplicate definition id registration must fail")
	}
	// Duplicate registration of the same map under another id.
	if err := c.Register("other", shared); err == nil {
		t.Fatal("registering the same map twice must fail")
	}

	// Duplicate definition inside a stream.
	stream := `{"a":{"$def":"x","$val":{"k":1}},"b":{"$def":"x","$val":{"k":2}}}`
	if _, err := c.Decode([]byte(stream)); err == nil ||
		!strings.Contains(err.Error(), "duplicate definition") {
		t.Fatalf("duplicate definition in stream must fail, got %v", err)
	}
}

func TestUnknownReferenceRejected(t *testing.T) {
	c := mapref.New()
	stream := `{"a":{"$ref":"never-defined"}}`
	if _, err := c.Decode([]byte(stream)); err == nil ||
		!strings.Contains(err.Error(), "unknown reference") {
		t.Fatalf("unknown reference must fail, got %v", err)
	}
}

func TestEncodeDoesNotMutateInput(t *testing.T) {
	root, shared := sharedGraph()
	snapshot := deepCopy(root)

	c := mapref.New()
	if err := c.Register("defaults/v1", shared); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := c.Encode(root); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !reflect.DeepEqual(root, snapshot) {
		t.Fatalf("input mutated by encode:\n got: %#v\nwant: %#v", root, snapshot)
	}
	for k := range shared {
		if strings.HasPrefix(k, "$") {
			t.Fatalf("definition id leaked into input map: key %q", k)
		}
	}
}

func TestCyclicInputRejected(t *testing.T) {
	self := map[string]any{}
	self["self"] = self // self-cycle

	c := mapref.New()
	if err := c.Register("loop", self); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := c.Encode(map[string]any{"loop": self}); err == nil ||
		!strings.Contains(err.Error(), "cyclic") {
		t.Fatalf("cyclic input must fail, got %v", err)
	}
}

func TestUnsupportedValuesRejected(t *testing.T) {
	c := mapref.New()
	if _, err := c.Encode(map[string]any{"slice": []any{1, 2}}); err == nil {
		t.Fatal("slice values must be rejected")
	}
	type someStruct struct{ X int }
	if _, err := c.Encode(map[string]any{"st": someStruct{X: 1}}); err == nil {
		t.Fatal("struct values must be rejected")
	}
	if _, err := c.Encode(map[string]any{"$def": "x"}); err == nil {
		t.Fatal("reserved keys must be rejected")
	}
}

func deepCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			// Shared maps stay shared in the snapshot so DeepEqual on the
			// original remains meaningful; copy their contents by value.
			out[k] = deepCopyShallow(sub)
			continue
		}
		out[k] = v
	}
	return out
}

func deepCopyShallow(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
