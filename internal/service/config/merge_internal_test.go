// Package config — white-box tests for the layer merge. The merge is where a
// later source overrides an earlier one, and the subtle failure is not a wrong
// value but a SHARED map: two layers pointing at one nested map means merging
// the second mutates the first source's own returned data, a side effect that
// outlives the call.
package config

import (
	"reflect"
	"testing"
)

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
		{
			//: an array REPLACES: merging element by element would produce a
			//: list no layer wrote. Copying arrays must never turn into this.
			name: "an array replaces an array, whole",
			dst:  map[string]any{"xs": []any{1, 2}, "n": map[string]any{"ys": []any{"a", "b"}}},
			src:  map[string]any{"xs": []any{3}, "n": map[string]any{"ys": []any{"c"}}},
			want: map[string]any{"xs": []any{3}, "n": map[string]any{"ys": []any{"c"}}},
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

// Test_cloneNested pins that the copy shares no map and no array with its
// source at any depth, and that leaves are carried across unchanged.
//
// Arrays are copied because a clone is not only read by the merge, which never
// mutates one: Source().Load hands a clone to a CALLER, who may. The array
// checks below read the SOURCE directly rather than through a second clone —
// a snapshot taken with the function under test would alias exactly as the
// clone does, and see nothing.
//
// MUTATION (2026-09-11): cloneNested put back to copying maps only, arrays
// stored by reference (HEAD's merge.go). Observed: `writing to an array in the
// clone reached the source` on `a slice`; on `an array of tables`, that and
// `writing to a table inside an array in the clone reached the source`. The
// nil-array case stayed green — sharing a nil array keeps it nil. Restored;
// SHA-256 of merge.go identical to the pre-mutation file.
//
// MUTATION (2026-09-11): cloneArray's slices.Clone replaced by `append([]any{},
// src...)`, which turns a nil array into an empty one. Observed on `a nil
// array`, from both checks: `key "xs" = []interface {}{}, want []interface
// {}(nil)` and `a nil array: the clone is []interface {}{}, want the nil array
// kept nil — null would decode as []`. Restored; SHA-256 identical.
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
		{"a slice", map[string]any{"xs": []any{1, 2}}},
		{"an array of tables", map[string]any{"xs": []any{map[string]any{"a": 1}, []any{"deep"}}}},
		{"a nil array", map[string]any{"xs": []any(nil)}},
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
		assertArrayDetached(t, got, c.src)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertArrayDetached checks the array under "xs", when there is one: a null
// stays null, a table inside it is its own, and so is the array itself.
func assertArrayDetached(t *testing.T, got, src map[string]any) {
	t.Helper()
	srcXs, isArray := src["xs"].([]any)
	//: nothing to check without an array.
	if !isArray {
		return
	}
	gotXs, _ := got["xs"].([]any)
	//: null must stay null — [] is a different document.
	if srcXs == nil {
		if gotXs != nil {
			t.Errorf("a nil array: the clone is %#v, want the nil array kept nil — null would decode as []", gotXs)
		}
		return
	}
	//: a table inside the array, written through the clone.
	if inner, isTable := gotXs[0].(map[string]any); isTable {
		inner["injected"] = true
		if srcInner, _ := srcXs[0].(map[string]any); srcInner["injected"] != nil {
			t.Error("writing to a table inside an array in the clone reached the source")
		}
	}
	//: the array's own element, written through the clone.
	gotXs[0] = "injected"
	if written, isString := srcXs[0].(string); isString && written == "injected" {
		t.Error("writing to an array in the clone reached the source")
	}
}

// assertNested compares two config maps structurally, recursing into nested
// maps and comparing everything else — arrays included — with
// reflect.DeepEqual, which also tells a nil array from an empty one. Arrays
// used to be skipped outright, which made "an array replaces, it is not
// merged" a claim nothing here could check.
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
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("key %q = %#v, want %#v", key, gotVal, wantVal)
		}
	}
}
