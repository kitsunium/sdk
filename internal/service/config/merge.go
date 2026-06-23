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
		//: otherwise the source value overrides.
		dst[key] = val
	}
}
