// Package config — recursive layer merge.
package config

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

// cloneNested deep-copies a nested config map so the merged result shares no
// map with any source layer. Only maps are copied: scalars are immutable, and
// slices are treated as opaque leaf values (the merge never descends into them,
// so it cannot mutate one).
func cloneNested(src map[string]any) map[string]any {
	//: exact-size destination; nested maps recurse.
	out := make(map[string]any, len(src))
	//: copy each entry, recursing only into nested maps.
	for key, val := range src {
		//: a nested map needs its own copy too.
		if sub, ok := val.(map[string]any); ok {
			//: recurse to detach the whole subtree.
			out[key] = cloneNested(sub)
			continue
		}
		//: leaves are copied by value / by reference-as-opaque.
		out[key] = val
	}
	//: fully detached copy.
	return out
}
