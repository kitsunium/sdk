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
// prefix and lower-casing the key. An EMPTY prefix — and only an empty prefix —
// reads every variable.
//
// The separating underscore is supplied by the Source, and a trailing one on
// prefix is absorbed rather than doubled: EnvSource("APP") and EnvSource("APP_")
// name the same namespace. Both spellings are natural to write, and the second
// used to build the prefix "APP__", which matches nothing — Load then returned
// an empty configuration and a nil error, so a typo cost the caller the whole
// config with no signal at all.
//
// That absorption can never empty a non-empty prefix. "" is not "a namespace
// whose name is empty", it is the whole environment, so a prefix made only of
// underscores names the "_" namespace instead of collapsing onto it.
func EnvSource(prefix string) coreconfig.Source {
	//: a stateless reader — safe to share.
	return envSource{prefix: prefix}
}

// Load scans the environment for the prefix and returns the stripped map.
func (s envSource) Load() (values map[string]any, err error) {
	//: the match prefix is "PREFIX_" (or "" for every variable).
	match := matchPrefix(s.prefix)
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
		//: coerce: a value that is one whole JSON document ("8080", "true")
		//: adopts its typed form; anything else ("kitsune", "0A0A01", "1500ms")
		//: stays the exact string it was. Map storage of the any is the
		//: linter-exempt sink for the dynamic value.
		//: coerceEnvValue keeps integers integral — a plain json.Unmarshal into
		//: an any yields float64 for EVERY numeric token, which silently breaks
		//: the "8080 becomes an integer" contract stated above.
		coerceEnvInto(out, short, val)
	}
	//: env reads never fail.
	return out, nil
}

// Describe implements core/config.Describer: the layer is the environment and
// the detail is the VARIABLE that supplied key — for a nested key, the one
// that supplied its top-level table. It is the variable Load read: the same
// match, and the last matching entry wins exactly as it does in Load.
//
// The value is never read into the answer; the name is what an operator types.
func (s envSource) Describe(key string) (layer, detail string) {
	top, _, _ := strings.Cut(key, ".")
	variable, _, found := s.lookup(top)
	//: the environment changed since the load, or the key never came from
	//: here — say which variable WOULD carry it.
	if !found {
		//: the canonical spelling under this prefix.
		return coreconfig.LayerEnv, matchPrefix(s.prefix) + strings.ToUpper(top)
	}
	//: the variable that set the key.
	return coreconfig.LayerEnv, variable
}

// lookup finds the environment variable that supplies the top-level key under
// this source's prefix, with its RAW value — before any JSON coercion. When
// several variables map to one key (APP_PORT and APP_port), the last one in
// the environment wins, which is the one Load stored.
func (s envSource) lookup(key string) (variable, raw string, found bool) {
	match := matchPrefix(s.prefix)
	//: every entry, in the order Load folds them.
	for _, kv := range os.Environ() {
		name, val, ok := strings.Cut(kv, "=")
		//: a match is exactly what Load would have stored under key.
		if ok && strings.HasPrefix(name, match) && strings.EqualFold(strings.TrimPrefix(name, match), key) {
			variable, raw, found = name, val, true
		}
	}
	//: the last match, or none.
	return variable, raw, found
}

// matchPrefix turns a caller-supplied prefix into the string every environment
// key must start with.
//
// An empty prefix is the explicit read-everything mode and maps to "". Any
// other prefix names a namespace and has to keep naming one, which is why the
// trailing-separator absorption is not a bare TrimRight: trimming "_" or "___"
// yields "", and "" is not a namespace with an empty name — it matches every
// key in the process environment. A caller who wrote a prefix asked to be
// scoped, so an all-underscore prefix resolves to the "_" namespace (the
// leading-underscore variables) rather than to everything.
func matchPrefix(prefix string) string {
	//: the all-variables mode, reachable only by writing no prefix at all.
	if prefix == "" {
		//: every key starts with "".
		return ""
	}
	//: absorb trailing separators — the Source supplies exactly one itself.
	name := strings.TrimRight(prefix, "_")
	//: an all-underscore prefix has no name part left; it is the "_" namespace,
	//: and must not fall through to the read-everything mode above.
	if name == "" {
		//: the separator itself is the namespace.
		return "_"
	}
	//: keys look like PREFIX_NAME.
	return name + "_"
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
//
// Coercion is gated on the value being ONE COMPLETE JSON document, because
// json.Decoder.Decode stops at the end of the first token and reports no error
// for whatever trails it. Without the gate every value that merely STARTS like
// a number is silently truncated to that prefix: "0A0A01" becomes 0, "1500ms"
// becomes 1500, "5gc.svc.cluster.local" becomes 5, "10.45.0.0/16" becomes
// 10.45. The result is not a parse failure the caller can see — it is a
// plausible-looking value of the wrong type, which either lands in the target
// silently or fails the whole Load with a decode error naming nothing.
// json.Valid spans the entire input, which is exactly the missing property; it
// still tolerates surrounding whitespace, so " 8080 " keeps coercing as before.
func coerceEnvInto(out map[string]any, key, val string) {
	//: anything that is not one complete JSON document keeps its raw text.
	if !json.Valid([]byte(val)) {
		//: a partial or trailing-garbage value is a string, never a prefix of one.
		out[key] = val
		//: nothing to classify.
		return
	}
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
