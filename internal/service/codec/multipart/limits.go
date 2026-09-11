// Package multipart — the memory bound applied to every decode AND every
// encode. A multipart body is the shape an upload arrives in, so the decoder
// is the SDK surface most exposed to a hostile stream: unbounded, a single
// crafted part drives io.ReadAll to an OOM, and a boundary flood does the same
// with a very large number of empty parts. Three bounds close both doors
// (per-part bytes, part count, aggregate bytes); the encoder enforces the
// identical set so the codec never emits a body it would refuse to read back.
package multipart

import "github.com/kitsunium/sdk/internal/kernel/errs"

// DefaultMaxPartBytes caps one part body at 32 MiB. Generous for a form field
// or a document upload, and small enough that a single part cannot exhaust a
// container's memory budget. Callers moving larger objects raise the bound
// explicitly through NewWithLimits.
const DefaultMaxPartBytes int64 = 32 << 20 // 32 MiB

// DefaultMaxParts caps a body at 1024 parts. A boundary flood costs the
// attacker one header line per part while costing the decoder a header parse
// and a slice entry each, so the part COUNT needs its own ceiling — the byte
// bounds alone do not constrain it on a streaming reader.
const DefaultMaxParts int = 1024

// DefaultMaxTotalBytes caps the sum of every part body at 64 MiB. Without it
// the real ceiling would be DefaultMaxPartBytes × DefaultMaxParts, which is
// not a bound anybody chose.
const DefaultMaxTotalBytes int64 = 64 << 20 // 64 MiB

// LimitsConfig bounds what a multipart body may materialise. Every field is a
// ceiling, never a target.
//
// A ZERO field means "use this package's documented default" — never
// "unlimited" (ADR 0031: a policy's zero value is a safe default or an
// explicit refusal, never an inert policy). A NEGATIVE field is refused at
// construction with LimitsInvalid: the SDK can supply a ceiling the caller
// forgot, but it cannot tell a typo apart from a request for no ceiling at
// all, and guessing would silently remove the guard the type exists to place.
//
// There is deliberately no way to spell "unlimited". A decoder without a
// ceiling is the defect this type prevents.
type LimitsConfig struct {
	// MaxPartBytes caps a single part body; 0 selects DefaultMaxPartBytes.
	MaxPartBytes int64
	// MaxParts caps the number of parts; 0 selects DefaultMaxParts.
	MaxParts int
	// MaxTotalBytes caps the sum of part bodies; 0 selects DefaultMaxTotalBytes.
	MaxTotalBytes int64
}

// resolve returns l with every zero field replaced by its documented default,
// or a typed LimitsInvalid error when any field is negative.
//
// Refusal happens HERE, at construction, rather than at first use: unlike the
// resilience constructors ADR 0031 amends, NewWithLimits is a new API with no
// existing call sites, so the (value, error) shape the ADR records as the
// preferred v2 form costs nothing to adopt now.
func (l LimitsConfig) resolve() (resolved LimitsConfig, err error) {
	//: name the offending knob so the refusal is actionable without echoing
	//: the value (ADR 0031: a refusal names the knob, never the value).
	knob, bad := l.negativeKnob()
	//: a negative bound is neither a limit nor a request for the default.
	if bad {
		//: typed refusal — matchable with errs.HasCode(err, CodeMultipartLimitsInvalid).
		return LimitsConfig{}, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeMultipartLimitsInvalid,
			Reason:  "LIMITS_INVALID",
			Public:  "multipart limits are not usable",
			Private: "service/codec/multipart.NewWithLimits: a negative bound is not a limit",
		}, errs.String("knob", knob))
	}
	//: every zero collapses to the package default; no field can stay inert.
	return LimitsConfig{
		MaxPartBytes:  clampInt64(l.MaxPartBytes, DefaultMaxPartBytes),
		MaxParts:      clampInt(l.MaxParts, DefaultMaxParts),
		MaxTotalBytes: clampInt64(l.MaxTotalBytes, DefaultMaxTotalBytes),
	}, nil
}

// negativeKnob reports the first field carrying a negative bound, in
// declaration order, so a struct with two mistakes still names one of them.
func (l LimitsConfig) negativeKnob() (knob string, found bool) {
	//: per-part ceiling.
	if l.MaxPartBytes < 0 {
		//: caller passed a negative byte cap.
		return "MaxPartBytes", true
	}
	//: part-count ceiling.
	if l.MaxParts < 0 {
		//: caller passed a negative count cap.
		return "MaxParts", true
	}
	//: aggregate ceiling.
	if l.MaxTotalBytes < 0 {
		//: caller passed a negative aggregate cap.
		return "MaxTotalBytes", true
	}
	//: every field is zero-or-positive — nothing to refuse.
	return "", false
}

// clampInt64 returns v when the caller chose one, else the supplied default.
func clampInt64(v, fallback int64) int64 {
	//: zero is "I did not choose" — hand back the documented default.
	if v == 0 {
		//: never unlimited.
		return fallback
	}
	//: caller-chosen ceiling.
	return v
}

// clampInt returns v when the caller chose one, else the supplied default.
func clampInt(v, fallback int) int {
	//: zero is "I did not choose" — hand back the documented default.
	if v == 0 {
		//: never unlimited.
		return fallback
	}
	//: caller-chosen ceiling.
	return v
}

// limitExceeded builds the typed LimitExceeded error for knob, carrying the
// ceiling and the observed magnitude as fields.
func limitExceeded(knob string, bound, got int64) error {
	//: typed sentinel so callers route on CodeMultipartLimitExceeded.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeMultipartLimitExceeded,
		Reason:  "LIMIT_EXCEEDED",
		Public:  "multipart payload exceeds a configured limit",
		Private: "service/codec/multipart: bound crossed while walking the parts",
	}, errs.String("knob", knob), errs.Int64("bound", bound), errs.Int64("got", got))
}
