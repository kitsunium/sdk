// Package config — recursive layer merge.
package config

import "slices"

// deepMerge folds src into dst in place: nested maps merge recursively, every
// other value (including a non-map overriding a map) overwrites. Later sources
// therefore win key-by-key.
func deepMerge(dst, src map[string]any) {
	//: fold each source key into the destination.
	for key, val := range src {
		//: two maps under the same key merge recursively.
		if sub, ok := val.(map[string]any); ok {
			//: only recurse when the destination side is also a map.
			if existing, ok := dst[key].(map[string]any); ok {
				//: merge the nested layers.
				deepMerge(existing, sub)
				continue
			}
		}
		//: a map arriving where dst has no map must be CLONED, not aliased.
		//: Storing src's map by reference means a later layer merging the same
		//: key mutates the earlier source's own returned map — a cross-layer
		//: side effect that outlives this call.
		if sub, ok := val.(map[string]any); ok {
			//: deep copy so later merges cannot reach back into the source.
			dst[key] = cloneNested(sub)
			continue
		}
		//: otherwise the source value overrides.
		dst[key] = val
	}
}

// cloneNested deep-copies a nested config map so the copy shares no table AND
// no array with its source, at any depth.
//
// Arrays are copied as well as maps — and a table inside an array with them.
// The merge never mutates an array (an array REPLACES, it is not merged), and
// that is why arrays were once treated as opaque leaves. But the merge is not
// the only reader of a copy: Source().Load hands one to the CALLER, and a
// caller who edited an array default it had been handed was editing the
// compiled schema's own value, so every later load decoded the edit.
//
// A table and an array are the two shapes a decoded layer holds by reference;
// everything else — a string, a bool, a json.Number, nil — is immutable and
// shared as it is.
func cloneNested(src map[string]any) map[string]any {
	//: exact-size destination.
	out := make(map[string]any, len(src))
	//: every entry, once.
	for key, val := range src {
		//: the two reference shapes are copied, the rest shared.
		switch typed := val.(type) {
		//: a table: recurse to detach the whole subtree.
		case map[string]any:
			out[key] = cloneNested(typed)
		//: an array: its own backing array, and its own tables.
		case []any:
			out[key] = cloneArray(typed)
		//: a scalar: nothing to detach.
		default:
			out[key] = val
		}
	}
	//: fully detached copy.
	return out
}

// cloneArray deep-copies an array so neither its backing store nor a table
// inside it is shared with src.
//
// A nil array stays nil — slices.Clone preserves that, and it matters: nil
// marshals as null, while an empty copy would marshal as [], a different
// document that decodes to a different value.
func cloneArray(src []any) []any {
	//: a new backing array carrying every scalar already, nil kept nil.
	out := slices.Clone(src)
	//: then every element, once.
	for i, element := range out {
		//: detach the two shapes an element can hold by reference.
		switch typed := element.(type) {
		//: a table inside the array.
		case map[string]any:
			out[i] = cloneNested(typed)
		//: an array inside the array.
		case []any:
			out[i] = cloneArray(typed)
		}
	}
	//: a detached array, in the same order — an array replaces, so its order
	//: is part of its value.
	return out
}
