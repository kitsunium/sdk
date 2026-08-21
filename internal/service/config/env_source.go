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
	out := make(map[string]any, len(os.Environ()))
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
		//: coerceEnvValue keeps integers integral — a plain json.Unmarshal into
		//: an any yields float64 for EVERY numeric token, which silently breaks
		//: the "8080 becomes an integer" contract stated above.
		coerceEnvInto(out, short, val)
	}
	//: env reads never fail.
	return out, nil
}

// coerceEnvInto parses val as a JSON token and stores it under key in out,
// falling back to the original string when it is not valid JSON.
//
// It writes into the map rather than returning an `any` so the dynamic value
// lives only at the map-storage site — the shape KTN-INTERFACE-ANYUSE exempts.
//
// It decodes with UseNumber so numeric tokens arrive as json.Number rather than
// float64. Decoding into a bare `any` makes encoding/json widen every number to
// float64, so "8080" would become 8080.0 — losing integrality, and with it
// exact values above 2^53. Integral tokens therefore become int64; only genuine
// fractions fall through to float64.
func coerceEnvInto(out map[string]any, key, val string) {
	//: decode from the raw bytes with number widening disabled.
	dec := json.NewDecoder(strings.NewReader(val))
	dec.UseNumber()
	//: parsed holds whatever token the value turned out to be.
	var parsed any
	//: a non-JSON value ("kitsune") keeps its original string form.
	if err := dec.Decode(&parsed); err != nil {
		//: not JSON — the bare word is the value.
		out[key] = val
		//: nothing further to classify.
		return
	}
	//: only numbers need the widening decision; everything else passes through.
	num, isNum := parsed.(json.Number)
	//: a non-numeric token is already in its final Go form.
	if !isNum {
		//: bool / null / array / object / string, already correctly typed.
		out[key] = parsed
		//: stored as decoded.
		return
	}
	//: an integral token stays an integer, preserving exactness past 2^53.
	if i, intErr := num.Int64(); intErr == nil {
		//: integral value.
		out[key] = i
		//: integral form stored.
		return
	}
	//: a genuine fraction (or an out-of-int64-range value) becomes a float.
	if f, floatErr := num.Float64(); floatErr == nil {
		//: fractional value.
		out[key] = f
		//: fractional form stored.
		return
	}
	//: unparseable as either — keep the original text rather than guessing.
	out[key] = val
}
