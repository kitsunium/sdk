// Package logger — WithError decomposes an SDK typed error into structured log
// Attrs (error.code / error.reason / error.public + wrap-trail codes), making
// the SDK's dotted-quad errors first-class structured data rather than a flat
// string. It lives beside the Attr constructors it produces.
package logger

import (
	"strconv"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// errorAttrCap is the small pre-allocation hint for an SDK error's Attr slice —
// code + reason + public + one trail frame covers the common case.
const errorAttrCap int = 4

// Structured keys WithError emits, documented in one place so the producer here
// and any downstream parser cannot drift.
const (
	// errorCodeKey carries the dotted-quad classification of an SDK error.
	errorCodeKey = "error.code"
	// errorReasonKey carries the SCREAMING_SNAKE reason of an SDK error.
	errorReasonKey = "error.reason"
	// errorPublicKey carries the wire-safe public message of an SDK error.
	errorPublicKey = "error.public"
	// errorTrailKey is the prefix for each positional wrap-trail code field.
	errorTrailKey = "error.trail"
	// errorMessageKey carries the flat Error() string of a non-SDK error.
	errorMessageKey = "error.message"
)

// WithError decomposes err into structured log Attrs so the SDK's dotted-quad
// typed errors become first-class structured data rather than a flat string.
//
// For an SDK error it emits error.code, error.reason and error.public, then one
// error.trail.<n> Attr per wrap-trail code (newest wrap last). Empty metadata
// fields are skipped. For a plain stdlib error it falls back to a single
// error.message Attr. A nil err yields no Attrs.
//
// The result is meant to be spread into an emission call, e.g.
// logger.Error(ctx, lg, "op failed", logger.WithError(err)...).
func WithError(err error) []Attr {
	//: a nil error carries nothing to decompose.
	if err == nil {
		//: no error means no fields to emit.
		return nil
	}

	code, ok := kerrs.CodeOf(err)

	//: no SDK code means err never passed through errs.Define/Wrap.
	if !ok {
		//: the flat message is the only field a non-SDK error can offer.
		return fallbackAttrs(err)
	}

	attrs := make([]Attr, 0, errorAttrCap)
	attrs = append(attrs, String(errorCodeKey, code.String()))
	attrs = appendReason(attrs, err)
	attrs = appendOptional(attrs, errorPublicKey, kerrs.PublicOf(err))
	attrs = appendTrail(attrs, kerrs.TrailOf(err))

	//: hand back the fully decomposed SDK-error record.
	return attrs
}

// fallbackAttrs renders a plain stdlib error as a single error.message Attr —
// the only field a non-SDK error can offer.
func fallbackAttrs(err error) []Attr {
	//: a non-SDK error contributes only its flat Error() string.
	return []Attr{String(errorMessageKey, err.Error())}
}

// appendReason appends the SCREAMING_SNAKE reason Attr when err carries one,
// returning the slice unchanged otherwise.
func appendReason(attrs []Attr, err error) []Attr {
	reason, ok := kerrs.ReasonOf(err)
	//: skip absent reasons so the record stays terse.
	if !ok {
		//: nothing to add when the error carries no reason.
		return attrs
	}

	//: a present reason joins the record under its dedicated key.
	return appendOptional(attrs, errorReasonKey, reason)
}

// appendOptional appends a string Attr under key only when value is non-empty,
// keeping empty error metadata out of the emitted record.
func appendOptional(attrs []Attr, key, value string) []Attr {
	//: skip empty values so the record stays terse.
	if value == "" {
		//: nothing to add when the value is empty.
		return attrs
	}

	//: a non-empty value joins the record under key.
	return append(attrs, String(key, value))
}

// appendTrail emits one indexed error.trail Attr per wrap-trail code, in the
// order TrailOf reports them (newest wrap last), and returns the grown slice.
func appendTrail(attrs []Attr, trail []kerrs.Code) []Attr {
	//: an empty trail adds nothing.
	for i, c := range trail {
		key := errorTrailKey + "." + strconv.Itoa(i)
		attrs = append(attrs, String(key, c.String()))
	}

	//: hand back the slice grown by one Attr per trail frame.
	return attrs
}
