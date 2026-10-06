// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// mapRefPair is the minimal graph fixture: two fields pointing at maps.
type mapRefPair struct {
	Left  map[string]string `json:"left"`
	Right map[string]string `json:"right"`
	Note  string            `json:"note"`
}

func TestMapRefSharedMapRoundTrip(t *testing.T) {
	shared := map[string]string{"k": "v", "a": "b"}
	reg := NewMapRefRegistry()
	if err := reg.Register("shared-1", shared); err != nil {
		t.Fatalf("register: %v", err)
	}
	in := mapRefPair{Left: shared, Right: shared, Note: "plain"}

	enc := NewMapRefEncoder(reg)
	data, err := enc.Encode(&in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"$def":"shared-1"`) {
		t.Fatalf("encoded stream missing definition: %s", s)
	}
	if !strings.Contains(s, `"$ref":"shared-1"`) {
		t.Fatalf("encoded stream missing reference: %s", s)
	}
	if strings.Count(s, `"shared-1"`) != 2 {
		t.Fatalf("expected exactly one definition and one reference: %s", s)
	}

	var out mapRefPair
	if err := NewMapRefDecoder().Decode(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(out.Left, shared) || !reflect.DeepEqual(out.Right, shared) {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if out.Note != "plain" {
		t.Fatalf("plain field mismatch: %q", out.Note)
	}
	// identity: both fields must point at the same map object
	if reflect.ValueOf(out.Left).Pointer() != reflect.ValueOf(out.Right).Pointer() {
		t.Fatalf("left and right do not share the same map object")
	}
	out.Left["k"] = "mutated"
	if out.Right["k"] != "mutated" {
		t.Fatalf("mutation through left not visible through right")
	}
}

func TestMapRefInputNotMutated(t *testing.T) {
	shared := map[string]string{"k": "v"}
	before := map[string]string{"k": "v"}
	reg := NewMapRefRegistry()
	if err := reg.Register("id-1", shared); err != nil {
		t.Fatalf("register: %v", err)
	}
	in := mapRefPair{Left: shared, Right: shared}
	if _, err := NewMapRefEncoder(reg).Encode(&in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !reflect.DeepEqual(shared, before) {
		t.Fatalf("input map was modified: %v", shared)
	}
	for k := range shared {
		if strings.HasPrefix(k, "$") {
			t.Fatalf("id was written into the input map: %q", k)
		}
	}
}

func TestMapRefDuplicateDefinitionRejected(t *testing.T) {
	stream := []byte(`{"left":{"$def":"dup","$val":{"k":"v"}},"right":{"$def":"dup","$val":{"k":"w"}}}`)
	var out mapRefPair
	err := NewMapRefDecoder().Decode(stream, &out)
	if err == nil {
		t.Fatal("expected duplicate definition to fail")
	}
	if !strings.Contains(err.Error(), "duplicate definition") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMapRefUnknownReferenceRejected(t *testing.T) {
	stream := []byte(`{"left":{"$ref":"never-defined"},"right":{"$ref":"never-defined"}}`)
	var out mapRefPair
	err := NewMapRefDecoder().Decode(stream, &out)
	if err == nil {
		t.Fatal("expected unknown reference to fail")
	}
	if !strings.Contains(err.Error(), "unknown reference") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMapRefUnregisteredMapRejected(t *testing.T) {
	in := mapRefPair{Left: map[string]string{"k": "v"}}
	_, err := NewMapRefEncoder(NewMapRefRegistry()).Encode(&in)
	if err == nil {
		t.Fatal("expected unregistered map to fail")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMapRefRegisterErrors(t *testing.T) {
	reg := NewMapRefRegistry()
	m := map[string]string{"k": "v"}
	if err := reg.Register("id-1", m); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register("id-1", map[string]string{"x": "y"}); err == nil {
		t.Fatal("expected duplicate definition id to fail")
	}
	if err := reg.Register("id-2", m); err == nil {
		t.Fatal("expected re-registering the same map to fail")
	}
	if err := reg.Register("id-3", "not a map"); err == nil {
		t.Fatal("expected registering a non-map to fail")
	}
	if err := reg.Register("id-4", map[string]string(nil)); err == nil {
		t.Fatal("expected registering a nil map to fail")
	}
	if err := reg.Register("", map[string]string{"k": "v"}); err == nil {
		t.Fatal("expected empty definition id to fail")
	}
}

// ExampleMapRefEncoder is a minimal graph example: a struct whose two
// fields point at the same registered map encodes as one definition and
// one reference, and decodes back into fields sharing one map object.
func ExampleMapRefEncoder() {
	shared := map[string]string{"k": "v"}
	reg := NewMapRefRegistry()
	if err := reg.Register("shared-1", shared); err != nil {
		panic(err)
	}
	in := mapRefPair{Left: shared, Right: shared}

	data, err := NewMapRefEncoder(reg).Encode(&in)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(data))

	var out mapRefPair
	if err := NewMapRefDecoder().Decode(data, &out); err != nil {
		panic(err)
	}
	out.Left["k"] = "seen-through-both"
	fmt.Println(out.Right["k"])

	// Output:
	// {"left":{"$def":"shared-1","$val":{"k":"v"}},"right":{"$ref":"shared-1"},"note":""}
	// seen-through-both
}
