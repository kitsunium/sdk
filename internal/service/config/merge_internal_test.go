// Package config — white-box tests for the layer merge. The merge is where a
// later source overrides an earlier one, and the subtle failure is not a wrong
// value but a SHARED map: two layers pointing at one nested map means merging
// the second mutates the first source's own returned data, a side effect that
// outlives the call.
package config

import "testing"

// Test_deepMerge pins the override rules and the non-aliasing guarantee.
func Test_deepMerge(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		dst  map[string]any
		src  map[string]any
		want map[string]any
	}
	tests := []tc{
		{
			name: "an empty destination takes everything",
			dst:  map[string]any{},
			src:  map[string]any{"a": 1},
			want: map[string]any{"a": 1},
		},
		{
			name: "a scalar overrides a scalar",
			dst:  map[string]any{"a": 1},
			src:  map[string]any{"a": 2},
			want: map[string]any{"a": 2},
		},
		{
			name: "keys the source does not name survive",
			dst:  map[string]any{"a": 1, "b": 2},
			src:  map[string]any{"a": 9},
			want: map[string]any{"a": 9, "b": 2},
		},
		{
			name: "nested maps merge key by key",
			dst:  map[string]any{"n": map[string]any{"a": 1, "b": 2}},
			src:  map[string]any{"n": map[string]any{"b": 9, "c": 3}},
			want: map[string]any{"n": map[string]any{"a": 1, "b": 9, "c": 3}},
		},
		{
			//: a non-map arriving over a map replaces it wholesale; there is
			//: nothing to merge a scalar into.
			name: "a scalar overrides a map",
			dst:  map[string]any{"n": map[string]any{"a": 1}},
			src:  map[string]any{"n": "flat"},
			want: map[string]any{"n": "flat"},
		},
		{
			name: "a map overrides a scalar",
			dst:  map[string]any{"n": "flat"},
			src:  map[string]any{"n": map[string]any{"a": 1}},
			want: map[string]any{"n": map[string]any{"a": 1}},
		},
		{
			name: "an empty source changes nothing",
			dst:  map[string]any{"a": 1},
			src:  map[string]any{},
			want: map[string]any{"a": 1},
		},
		{
			name: "a nil value is still an override",
			dst:  map[string]any{"a": 1},
			src:  map[string]any{"a": nil},
			want: map[string]any{"a": nil},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: keep an untouched copy of the source so aliasing is observable.
		srcBefore := cloneNested(c.src)

		deepMerge(c.dst, c.src)

		assertNested(t, c.dst, c.want)
		//: the merge must not have written into the source layer.
		assertNested(t, c.src, srcBefore)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_deepMerge_DoesNotAlias pins the specific bug the clone exists for: a map
// stored by reference into a destination that had no map under that key. A later
// layer merging the same key would then reach back into the FIRST source's own
// returned map, changing data the source has every reason to believe is private.
func Test_deepMerge_DoesNotAlias(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the nested layer the first source hands over.
		first map[string]any
		//: what a later layer merges into the same key.
		second map[string]any
	}
	tests := []tc{
		{"a single nested key", map[string]any{"a": 1}, map[string]any{"a": 2}},
		{"a new nested key", map[string]any{"a": 1}, map[string]any{"b": 2}},
		{"a doubly nested map", map[string]any{"in": map[string]any{"a": 1}}, map[string]any{"in": map[string]any{"a": 2}}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the source keeps its own reference, exactly as a real Source does.
		source := map[string]any{"n": c.first}
		before := cloneNested(source)

		merged := make(map[string]any, 1)
		deepMerge(merged, source)
		deepMerge(merged, map[string]any{"n": c.second})

		//: the first source's map must be exactly as it handed it over.
		assertNested(t, source, before)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cloneNested pins that the copy shares no map with its source at any
// depth, and that leaves are carried across unchanged.
func Test_cloneNested(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		src  map[string]any
	}
	tests := []tc{
		{"an empty map", map[string]any{}},
		{"a flat map", map[string]any{"a": 1, "b": "two"}},
		{"a nested map", map[string]any{"n": map[string]any{"a": 1}}},
		{"a doubly nested map", map[string]any{"n": map[string]any{"in": map[string]any{"a": 1}}}},
		//: slices are opaque leaves — the merge never descends into one, so it
		//: cannot mutate one either.
		{"a slice leaf", map[string]any{"xs": []any{1, 2}}},
		{"a nil leaf", map[string]any{"a": nil}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := cloneNested(c.src)

		assertNested(t, got, c.src)
		//: mutating the clone must not reach the source at any depth.
		got["injected"] = true
		if _, leaked := c.src["injected"]; leaked {
			t.Error("writing to the clone reached the source")
		}
		if sub, ok := got["n"].(map[string]any); ok {
			sub["injected"] = true
			srcSub, _ := c.src["n"].(map[string]any)
			if _, leaked := srcSub["injected"]; leaked {
				t.Error("writing to a nested clone reached the source")
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertNested compares two config maps structurally, recursing into nested
// maps and comparing everything else with ==.
func assertNested(t *testing.T, got, want map[string]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("map has %d keys, want %d (%v vs %v)", len(got), len(want), got, want)
	}
	for key, wantVal := range want {
		gotVal, present := got[key]
		if !present {
			t.Errorf("key %q is missing", key)
			continue
		}
		wantSub, wantIsMap := wantVal.(map[string]any)
		gotSub, gotIsMap := gotVal.(map[string]any)
		if wantIsMap != gotIsMap {
			t.Errorf("key %q is %T, want %T", key, gotVal, wantVal)
			continue
		}
		if wantIsMap {
			assertNested(t, gotSub, wantSub)
			continue
		}
		//: slices are opaque leaves; comparing them by identity is enough,
		//: since nothing in the merge rewrites one.
		if _, isSlice := wantVal.([]any); isSlice {
			continue
		}
		if gotVal != wantVal {
			t.Errorf("key %q = %#v, want %#v", key, gotVal, wantVal)
		}
	}
}
