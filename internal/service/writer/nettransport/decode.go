// Package nettransport — the config Decoder making tcp/udp/http reachable from a
// config file via pkg/v1/logger.FromConfig. Only the plain-data keys are
// decodable (address / min_level / buffer_size); the Dialer and HTTPClient SSRF
// seams are code-only and never come from a config blob. A malformed shape
// yields the shared core/writer.WriterConfigInvalid (no per-package code), tagged
// with the protocol only — never the offending value (secret gate).
package nettransport

import (
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Decode builds a NetConfig from a raw config map, satisfying
// core/writer.Decoder. "address" is mandatory; "min_level" and "buffer_size"
// are optional. The code-only Dialer / HTTPClient seams are not decodable.
func (f *netFactory) Decode(raw map[string]any) (cfg writer.Config, err error) {
	//: accumulate into a zero config; absent optional keys keep their defaults.
	c := NetConfig{}
	//: one short-circuiting guard maps address (mandatory, non-empty), the
	//: optional severity floor, and the optional ring size; any malformed shape
	//: surfaces the redacted sentinel naming only the protocol, never a value.
	if !decodeStr(raw, "address", &c.Address) || c.Address == "" ||
		!decodeLevelKey(raw, &c.MinLevel) ||
		!decodeIntKey(raw, "buffer_size", &c.BufferSize) {
		//: redacted: a malformed value never reaches the error.
		return nil, f.invalid()
	}
	//: hand back the typed config Open type-asserts.
	return c, nil
}

// invalid returns the shared, redacted WriterConfigInvalid sentinel tagged with
// the protocol only — never a decoded option value (secret gate).
func (f *netFactory) invalid() error {
	//: attach the protocol (never an option value) for offender ID.
	return errs.Wrap(writer.WriterConfigInvalid, errs.WrapParams{}, errs.String("writer", f.proto))
}

// decodeStr maps an optional string key onto dst, reporting false only when the
// key is present with a non-string value. An absent key leaves dst untouched and
// reports true (the caller enforces mandatoriness separately).
func decodeStr(raw map[string]any, key string, dst *string) bool {
	//: an absent key is not an error here — the caller checks mandatoriness.
	v, present := raw[key]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its default; report success.
		return true
	}
	//: the value must be a string scalar.
	s, ok := v.(string)
	//: a non-string value is malformed.
	if !ok {
		//: report the shape failure.
		return false
	}
	//: assign the validated value.
	*dst = s
	//: mapped cleanly.
	return true
}

// decodeLevelKey maps the optional "min_level" name onto dst, reporting false on
// a non-string or unknown level. An absent key leaves dst untouched.
func decodeLevelKey(raw map[string]any, dst *level.Level) bool {
	//: absent min_level inherits the handler-global level (zero value).
	v, present := raw["min_level"]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its inherit default; report success.
		return true
	}
	//: the level must be a canonical name (string).
	s, ok := v.(string)
	//: a non-string level is malformed.
	if !ok {
		//: report the shape failure.
		return false
	}
	//: ParseLevel is redaction-safe — it returns a sentinel, never the input.
	lvl, perr := level.ParseLevel(s)
	//: an unknown level name is malformed.
	if perr != nil {
		//: report the shape failure.
		return false
	}
	//: apply the parsed floor.
	*dst = lvl
	//: mapped cleanly.
	return true
}

// decodeIntKey maps an optional integer key onto dst, accepting the numeric
// shapes a config codec yields (int / int64 / float64). An absent key leaves dst
// untouched; a present non-numeric value reports false.
func decodeIntKey(raw map[string]any, key string, dst *int) bool {
	//: an absent optional key keeps the existing default.
	v, present := raw[key]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its default; report success.
		return true
	}
	//: dispatch on the concrete decoded numeric type.
	switch t := v.(type) {
	//: native int (YAML scalar).
	case int:
		//: assign directly.
		*dst = t
	//: native int64 (CBOR signed).
	case int64:
		//: narrow to int for the buffer-size knob.
		*dst = int(t)
	//: JSON numbers decode to float64.
	case float64:
		//: truncate toward zero.
		*dst = int(t)
	//: any other shape is not a supported integer.
	default:
		//: report the shape failure.
		return false
	}
	//: mapped cleanly.
	return true
}
