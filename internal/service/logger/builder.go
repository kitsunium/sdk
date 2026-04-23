// Package logger: builder.go declares the chainable Builder API — the
// zero-allocation hot path for callers that care about per-call cost.
// Pulled from a buffer.Recycler[*chainBuilder], the builder accumulates
// attrs without allocating beyond the pre-sized scratchpad and returns to
// the pool on Send.
package logger

import (
	"context"
	"runtime"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// Builder is the chainable interface returned by Logger.Build. Every typed
// accessor (Str / Int / Bool / …) returns the receiver so callers compose
// the chain in a single expression. Send terminates the chain by emitting
// the accumulated record through the underlying handler and returns the
// builder to the recycler — callers MUST NOT use the builder after Send.
type Builder interface {
	// Str appends a string attribute.
	Str(key, val string) (out Builder)
	// Int appends an int attribute (widened to int64 internally).
	Int(key string, val int) (out Builder)
	// Int64 appends an int64 attribute.
	Int64(key string, val int64) (out Builder)
	// Uint64 appends a uint64 attribute.
	Uint64(key string, val uint64) (out Builder)
	// Bool appends a boolean attribute.
	Bool(key string, val bool) (out Builder)
	// Float64 appends a float64 attribute.
	Float64(key string, val float64) (out Builder)
	// Duration appends a time.Duration attribute.
	Duration(key string, val time.Duration) (out Builder)
	// Time appends a time.Time attribute.
	Time(key string, val time.Time) (out Builder)
	// Any appends an opaque attribute; handlers degrade unrecognised types to "?".
	Any(key string, val any) (out Builder)
	// Send terminates the chain by emitting the accumulated record.
	Send(ctx context.Context, msg string)
}

// chainBuilder is the default Builder implementation, recycled across Log
// calls via recordPool to keep the steady-state cost at zero allocations.
type chainBuilder struct {
	// owner is the loggerImpl that produced this builder; used to forward
	// the eventual Log call once the chain terminates.
	owner *loggerImpl
	// lv is the level chosen at Build time; the chain's terminal Send call
	// forwards it onto the underlying handler.
	lv level.Level
	// attrs is the accumulated attribute slice; pre-sized at construction
	// so the steady-state cost of Str/Int/etc. is zero allocations.
	attrs []corelogger.AttrValue
}

// Str appends a string attribute and returns the receiver for chaining.
//
// Params:
//   - key: attribute identifier rendered verbatim by handlers.
//   - val: string payload encoded by the typed Value constructor.
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Str(key, val string) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.StringValue(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Int appends an int attribute and returns the receiver for chaining.
//
// Params:
//   - key: attribute identifier.
//   - val: int payload widened to int64 by the typed Value constructor.
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Int(key string, val int) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.IntValue(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Int64 appends an int64 attribute.
//
// Params:
//   - key: attribute identifier.
//   - val: int64 payload encoded by the typed Value constructor.
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Int64(key string, val int64) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.Int64Value(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Uint64 appends a uint64 attribute.
//
// Params:
//   - key: attribute identifier.
//   - val: uint64 payload encoded by the typed Value constructor.
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Uint64(key string, val uint64) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.Uint64Value(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Bool appends a boolean attribute.
//
// Params:
//   - key: attribute identifier.
//   - val: boolean payload encoded by the typed Value constructor.
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Bool(key string, val bool) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.BoolValue(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Float64 appends a float64 attribute.
//
// Params:
//   - key: attribute identifier.
//   - val: float64 payload encoded by the typed Value constructor.
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Float64(key string, val float64) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.Float64Value(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Duration appends a time.Duration attribute.
//
// Params:
//   - key: attribute identifier.
//   - val: time.Duration payload encoded by the typed Value constructor.
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Duration(key string, val time.Duration) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.DurationValue(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Time appends a time.Time attribute.
//
// Params:
//   - key: attribute identifier.
//   - val: time.Time payload encoded by the typed Value constructor.
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Time(key string, val time.Time) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.TimeValue(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Any appends an opaque attribute. Use the typed helpers when possible.
//
// Params:
//   - key: attribute identifier.
//   - val: opaque payload; handlers degrade unrecognised types to "?".
//
// Returns:
//   - out: the receiver to enable method chaining.
func (b *chainBuilder) Any(key string, val any) (out Builder) {
	//: append the typed attribute onto the recycled scratchpad.
	b.attrs = append(b.attrs, corelogger.AttrValue{Key: key, Value: corelogger.AnyValue(val)})
	//: return the receiver so callers can chain further attribute calls.
	return b
}

// Send terminates the chain by emitting a RecordEvent through the owning
// Logger and returns the builder to the recycler. Callers MUST NOT use
// the builder after Send returns.
//
// Params:
//   - ctx: request-scoped context forwarded to the Handler.
//   - msg: human-readable message attached to the record.
func (b *chainBuilder) Send(ctx context.Context, msg string) {
	//: capture the application caller PC so handlers can render frames lazily.
	var pcs [1]uintptr
	runtime.Callers(callerSkipDepth, pcs[:])
	//: build the RecordEvent once so we can pass it to Enabled and Handle.
	r := corelogger.RecordEvent{Level: b.lv, Message: msg, PC: pcs[0], Attrs: b.attrs}
	//: short-circuit when the handler reports disabled — avoids formatting work.
	if b.owner.h.Enabled(ctx, r) {
		//: route any handler error through the documented swallow helper.
		swallowHandlerError(b.owner.h.Handle(ctx, r))
	}
	//: reset the scratchpad and return the builder to the recycler.
	b.attrs = b.attrs[:0]
	b.owner = nil
	recordPool.Put(b)
}
