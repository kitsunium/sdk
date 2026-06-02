// Package rotfile — the config Decoder that makes "rotfile" reachable from a
// config file via pkg/v1/logger.FromConfig. Kept in its own file (NOT inlined
// into rotfile.go) so the factory's Open and Decode halves never collide in one
// source. Every helper redacts: a malformed value yields RotFileDecodeFailed
// tagged with the writer name only, never the offending value (secret gate).
package rotfile

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultRotateRetentionDays is the retention floor applied when interval
// rotation is enabled (rotate_every > 0) but no explicit max_age_days is given:
// the "archive every 24h, keep 7 days" default. Without it an enabled archive
// policy would silently keep every rotated sibling forever.
const defaultRotateRetentionDays int = 7

// Decode builds a rotfile Config from a raw config map decoded from a config
// file, satisfying core/writer.Decoder. "path" is mandatory; every other key is
// optional and inherits the Config zero value. A present scalar of the wrong
// shape yields the redacted RotFileDecodeFailed sentinel. "rotate_every" is
// tolerant by contract: an unset, non-positive, or unparsable duration disables
// interval rotation (RotateEvery stays 0, never reaching worker.Every) rather
// than failing the whole topology; when it IS enabled and max_age_days is unset,
// retention defaults to defaultRotateRetentionDays.
func (*rotFileFactory) Decode(raw map[string]any) (cfg writer.Config, err error) {
	//: accumulate into a zero config; absent optional keys keep their defaults.
	c := Config{}
	//: the destination path is mandatory and must be a non-empty string.
	if perr := decodeReqString(raw, "path", &c.Path); perr != nil {
		//: redacted: names only the writer, never the decoded path.
		return nil, perr
	}
	//: map the optional scalar knobs (size / backups / age / compress / level).
	if serr := decodeRotScalars(raw, &c); serr != nil {
		//: the helper already returned the redacted sentinel.
		return nil, serr
	}
	//: tolerant: a bad/absent rotate_every leaves interval rotation disabled.
	decodeRotateEvery(raw, &c.RotateEvery)
	//: an enabled interval with no explicit retention defaults to 7 days so the
	//: archive policy never keeps siblings forever by omission.
	if c.RotateEvery > 0 && c.MaxAgeDays <= 0 {
		//: apply the documented retention floor.
		c.MaxAgeDays = defaultRotateRetentionDays
	}
	//: hand back the typed config Open type-asserts.
	return c, nil
}

// decodeRotScalars maps the optional scalar knobs (max_bytes, max_backups,
// max_age_days, compress, min_level) onto c, returning the redacted sentinel on
// the first malformed shape. Split from Decode so each stays within the
// cyclomatic-complexity cap.
func decodeRotScalars(raw map[string]any, c *Config) error {
	//: map the optional size threshold (int64).
	if berr := decodeInt64(raw, "max_bytes", &c.MaxBytes); berr != nil {
		//: redacted shape error.
		return berr
	}
	//: map the optional count cap.
	if berr := decodeInt(raw, "max_backups", &c.MaxBackups); berr != nil {
		//: redacted shape error.
		return berr
	}
	//: map the optional calendar-retention window.
	if aerr := decodeInt(raw, "max_age_days", &c.MaxAgeDays); aerr != nil {
		//: redacted shape error.
		return aerr
	}
	//: map the optional gzip toggle.
	if cerr := decodeBool(raw, "compress", &c.Compress); cerr != nil {
		//: redacted shape error.
		return cerr
	}
	//: map the optional severity floor (canonical level name).
	if lerr := decodeRotMinLevel(raw, &c.MinLevel); lerr != nil {
		//: redacted shape error.
		return lerr
	}
	//: every scalar mapped cleanly.
	return nil
}

// decodeReqString maps a mandatory non-empty string key onto dst. An absent
// key, a non-string value, or an empty string all yield the redacted sentinel.
func decodeReqString(raw map[string]any, key string, dst *string) error {
	//: a mandatory key's absence is a malformed shape, not a default.
	v, present := raw[key]
	//: reject a missing mandatory key (redacted).
	if !present {
		//: surface the redacted sentinel.
		return decodeInvalid()
	}
	//: the value must be a non-empty string scalar.
	s, ok := v.(string)
	//: a non-string or empty value is malformed (redacted).
	if !ok || s == "" {
		//: surface the redacted sentinel.
		return decodeInvalid()
	}
	//: assign the validated value.
	*dst = s
	//: mapped cleanly.
	return nil
}

// decodeInt64 maps an optional integer key onto dst, accepting the numeric
// shapes a config codec yields (int / int64 / uint64 / float64). An absent key
// leaves dst untouched; a present non-numeric value yields the redacted sentinel.
func decodeInt64(raw map[string]any, key string, dst *int64) error {
	//: an absent optional key keeps the existing default.
	v, present := raw[key]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its default.
		return nil
	}
	//: coerce the recognised numeric shapes to int64.
	n, ok := asInt64(v)
	//: a non-numeric value is a malformed shape (redacted).
	if !ok {
		//: surface the redacted sentinel.
		return decodeInvalid()
	}
	//: apply the coerced value.
	*dst = n
	//: mapped cleanly.
	return nil
}

// decodeInt maps an optional integer key onto an int dst, reusing the int64
// coercion and narrowing. An absent key leaves dst untouched.
func decodeInt(raw map[string]any, key string, dst *int) error {
	//: an absent optional key keeps the existing default.
	v, present := raw[key]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its default.
		return nil
	}
	//: coerce the recognised numeric shapes to int64 first.
	n, ok := asInt64(v)
	//: a non-numeric value is a malformed shape (redacted).
	if !ok {
		//: surface the redacted sentinel.
		return decodeInvalid()
	}
	//: narrow to int for the count/day knobs.
	*dst = int(n)
	//: mapped cleanly.
	return nil
}

// decodeBool maps an optional bool key onto dst. An absent key leaves dst
// untouched; a present non-bool value yields the redacted sentinel.
func decodeBool(raw map[string]any, key string, dst *bool) error {
	//: an absent optional key keeps the existing default.
	v, present := raw[key]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its default.
		return nil
	}
	//: the value must be a bool scalar.
	b, ok := v.(bool)
	//: a non-bool value is a malformed shape (redacted).
	if !ok {
		//: surface the redacted sentinel.
		return decodeInvalid()
	}
	//: apply the toggle.
	*dst = b
	//: mapped cleanly.
	return nil
}

// decodeRotMinLevel maps the optional "min_level" name onto dst. An absent key
// inherits the handler-global floor; a non-string or unknown name is redacted.
func decodeRotMinLevel(raw map[string]any, dst *level.Level) error {
	//: absent min_level inherits the handler-global level (zero value).
	v, present := raw["min_level"]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at its inherit default.
		return nil
	}
	//: the level must be a canonical name (string).
	s, ok := v.(string)
	//: a non-string level is malformed (redacted).
	if !ok {
		//: surface the redacted sentinel.
		return decodeInvalid()
	}
	//: ParseLevel is redaction-safe — it returns a sentinel, never the input.
	lvl, perr := level.ParseLevel(s)
	//: an unknown level name is malformed (redacted).
	if perr != nil {
		//: surface the redacted sentinel.
		return decodeInvalid()
	}
	//: apply the parsed floor.
	*dst = lvl
	//: mapped cleanly.
	return nil
}

// decodeRotateEvery maps the optional "rotate_every" duration onto dst. It is
// deliberately TOLERANT: an absent key, a non-string value, an unparsable
// duration, or a non-positive duration all leave dst at zero (interval rotation
// disabled) — a misconfigured archive cadence must not panic worker.Every nor
// fail the entire logging topology. It therefore returns no error.
func decodeRotateEvery(raw map[string]any, dst *time.Duration) {
	//: absent key → interval rotation stays disabled.
	v, present := raw["rotate_every"]
	//: nothing to map when the key is absent.
	if !present {
		//: leave dst at zero.
		return
	}
	//: only a string can carry a Go duration literal; ignore other shapes.
	s, ok := v.(string)
	//: a non-string rotate_every is tolerated as "disabled".
	if !ok {
		//: leave dst at zero.
		return
	}
	//: parse the duration; a malformed literal is tolerated as "disabled".
	dur, perr := time.ParseDuration(s)
	//: only a strictly positive, well-formed duration enables the ticker.
	if perr != nil || dur <= 0 {
		//: leave dst at zero so worker.Every is never reached.
		return
	}
	//: enable interval rotation at the parsed cadence.
	*dst = dur
}

// asInt64 coerces the numeric shapes a config codec may yield (int, int64,
// uint64, float64) to int64, reporting false for any other type. Centralising
// the coercion keeps every numeric decoder consistent across codecs (YAML emits
// int, JSON float64, CBOR int64/uint64).
func asInt64(v any) (n int64, ok bool) {
	//: dispatch on the concrete decoded numeric type.
	switch t := v.(type) {
	//: native int (YAML scalar).
	case int:
		//: widen to int64.
		return int64(t), true
	//: native int64 (CBOR signed).
	case int64:
		//: already the target width.
		return t, true
	//: CBOR unsigned integer.
	case uint64:
		//: accept within the int64 range.
		return int64(t), true
	//: JSON numbers decode to float64.
	case float64:
		//: truncate toward zero.
		return int64(t), true
	//: any other shape is not a supported integer.
	default:
		//: report the coercion miss.
		return 0, false
	}
}

// decodeInvalid returns the shared, redacted RotFileDecodeFailed sentinel tagged
// with the writer name only — never a decoded option value (secret gate).
// Origin-wins keeps errors.Is matching RotFileDecodeFailed through the wrap.
func decodeInvalid() error {
	//: attach the writer name (never an option value) for offender ID.
	return errs.Wrap(RotFileDecodeFailed, errs.WrapParams{}, errs.String("writer", "rotfile"))
}
