// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package mapref_test

import (
	"fmt"

	"github.com/ugorji/go/codec/mapref"
)

// Example shows the minimal directed graph: two services share one
// "defaults" map. After a round trip, both fields still point at the same
// object, so a change through one is visible through the other.
func Example() {
	defaults := map[string]any{"retries": 3, "timeout": "5s"}
	graph := map[string]any{
		"api":  map[string]any{"defaults": defaults},
		"jobs": map[string]any{"defaults": defaults},
	}

	c := mapref.New()
	if err := c.Register("defaults/v1", defaults); err != nil {
		panic(err)
	}
	data, err := c.Encode(graph)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(data))

	out, err := c.Decode(data)
	if err != nil {
		panic(err)
	}
	api := out["api"].(map[string]any)["defaults"].(map[string]any)
	jobs := out["jobs"].(map[string]any)["defaults"].(map[string]any)
	api["retries"] = 9 // mutate through one field ...
	fmt.Println(jobs["retries"])

	// Output:
	// {"api":{"defaults":{"$def":"defaults/v1","$val":{"retries":3,"timeout":"5s"}}},"jobs":{"defaults":{"$ref":"defaults/v1"}}}
	// 9
}
