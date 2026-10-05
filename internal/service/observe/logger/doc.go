// Package logger — declares the chainable Builder API — the low-allocation
// hot path for callers that care about per-call cost. Pulled from a
// recycler.Pool[*chainBuilder], the builder accumulates attrs without
// allocating beyond the pre-sized scratchpad and returns to the pool on
// Send. It is NOT allocation-free end to end: the handler clones the
// accumulated attrs on every Send, so exactly one slice escapes per emit
// (see pkg/v1/observe/logger/BENCH.md).
//
// Package logger — declares genericHandler — the corelogger.Handler
// implementation that composes an Encoder (format) with a Sink (transport).
// It replaces the legacy TextHandler that fused both responsibilities into
// one struct, and ships as the single Handler the service layer offers.
//
// Package logger — wires a core.Handler into a core.Logger. It
// provides the thin loggerImpl that routes Log calls through the Handler's
// Enabled fast-path and honours With-derived attrs via copy-on-write.
//
// Package logger — a Logger that owns the writers it was built over, and the
// one call that releases them.
//
// Package logger — declares the recycled event-record bucket consumed
// by the Builder API. Pulling RecordEvent values from a recycler.Pool
// removes the builder and scratchpad allocations from the hot path; the
// handler still clones the accumulated attrs on every Send, so the
// steady-state cost is ONE heap allocation per emit, not zero. Combined
// with the kind-discriminated Value union the remaining per-call cost is
// dominated by the encoder + sink rather than GC pressure.
//
// Package logger — declares the sourceHandler decorator — a Handler
// middleware that resolves the RecordEvent.PC captured by the front-end into a
// structured "source" attribute (file:line:function) before delegating to the
// wrapped Handler. It is additive and opt-in: the record shape is unchanged
// until a caller wraps a Logger via WithCaller, and resolution is stdlib-only
// (runtime.CallersFrames).
//
// Package logger — implements the TextHandler — a concrete
// core.Handler that renders RecordEvent values as
// "TIME LEVEL msg key=val..." and writes the bytes to an io.Writer under a
// mutex. It composes the kernel/concur/buffer pool and kernel/clock abstraction so
// that tests can drive it deterministically.
package logger
