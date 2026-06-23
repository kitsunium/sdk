// Package config — environment-variable Source.
package config

import (
	"encoding/json"
	"os"
	"strings"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
)

// envSource reads PREFIX_KEY environment variables into a flat lower-cased map.
type envSource struct {
	prefix string
}

// EnvSource returns a Source reading "PREFIX_KEY=value" env vars, stripping the
// prefix and lower-casing the key. An empty prefix reads every variable.
func EnvSource(prefix string) coreconfig.Source {
	//: a stateless reader — safe to share.
	return envSource{prefix: prefix}
}

// Load scans the environment for the prefix and returns the stripped map.
func (s envSource) Load() (values map[string]any, err error) {
	//: the match prefix is "PREFIX_" (or "" for every variable).
	match := s.prefix
	//: a non-empty prefix gains the separating underscore.
	if match != "" {
		//: keys look like PREFIX_NAME.
		match += "_"
	}
	//: collect the matching variables into a flat map.
	out := make(map[string]any)
	//: each environ entry is "KEY=VALUE".
	for _, kv := range os.Environ() {
		//: split on the first '='; skip malformed entries.
		key, val, ok := strings.Cut(kv, "=")
		//: a missing '=' is not a usable variable.
		if !ok {
			//: skip it.
			continue
		}
		//: only keys under the prefix contribute.
		if !strings.HasPrefix(key, match) {
			//: outside the namespace.
			continue
		}
		//: strip the prefix and lower-case the remaining key.
		short := strings.ToLower(strings.TrimPrefix(key, match))
		//: coerce: a JSON-parseable value ("8080", "true") adopts its typed form;
		//: a bare word ("kitsune") stays a string. Map storage of the any is the
		//: linter-exempt sink for the dynamic value.
		var parsed any
		//: try to read the value as a JSON token.
		if jsonErr := json.Unmarshal([]byte(val), &parsed); jsonErr == nil {
			//: typed (number/bool/null/array/object).
			out[short] = parsed
		} else {
			//: otherwise keep the original string.
			out[short] = val
		}
	}
	//: env reads never fail.
	return out, nil
}
