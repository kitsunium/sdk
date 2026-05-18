// Package logger: text_handler.go implements the TextHandler — a concrete
// core.Handler that renders RecordEvent values as
// "TIME LEVEL msg key=val ..." and writes the bytes to an io.Writer under a
// mutex. It composes the kernel/buffer pool and kernel/clock abstraction so
// that tests can drive it deterministically.
package logger

import (
	"context"
	"io"
	"strconv"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/buffer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// timestampLayout is the RFC3339-with-milliseconds format used to render the
// RecordEvent.Time field into the textual output.
const timestampLayout string = "2006-01-02T15:04:05.000Z07:00"

// decimalBase is the radix passed to strconv.Append* for human-readable ints.
const decimalBase int = 10

// floatPrec is the -1 sentinel that asks strconv to emit the shortest
// round-trippable representation for a float64.
const floatPrec int = -1

// floatBitSize tells strconv.AppendFloat we are formatting a 64-bit float.
const floatBitSize int = 64

// floatFormat is the byte verb ('g') selecting strconv's exponent-or-decimal
// mode for float64 rendering.
const floatFormat byte = 'g'

// groupSeparator is the rune inserted between successive group names AND
// between a group prefix and an attribute key in textual output ("a.b.k=v").
const groupSeparator byte = '.'

// TextHandler is a core.Handler that renders records as a single plain-text
// line per event. It is safe for concurrent use via an internal mutex.
type TextHandler struct {
	// w is the backing sink; writes are serialised through mu.
	w io.Writer
	// mu serialises Write calls so concurrent goroutines emit atomic lines.
	mu sync.Mutex
	// attrs are prepended to every RecordEvent emitted through this handler.
	attrs []corelogger.AttrValue
	// groups carries the active group prefix stack (outermost first); each
	// emitted attribute key is rendered as "g1.g2.….key" in the line.
	groups []string
	// min is the minimum level the handler emits; records below are dropped.
	min level.Level
	// clk sources the timestamp when RecordEvent.Time is the zero value.
	clk clock.Clock
}

// NewTextHandler constructs a TextHandler that writes to w and filters records
// strictly below min. The returned handler uses the real system clock.
//
// Params:
//   - w: destination writer; nil causes the constructor to reject the call.
//   - min: minimum level to emit.
//
// Returns:
//   - *TextHandler: a ready-to-use handler, or nil on rejection.
//   - error: WriterNil when w is nil; nil on success.
func NewTextHandler(w io.Writer, min level.Level) (h *TextHandler, err error) {
	//: reject nil writers early rather than panic at first write.
	if w == nil {
		//: caller supplied no sink — return the documented sentinel.
		return nil, WriterNil
	}
	//: construct a fresh handler bound to the real wall clock.
	return &TextHandler{w: w, min: min, clk: clock.System}, nil
}

// Enabled reports whether the handler would emit a record at r.Level, taking
// context cancellation into account.
//
// Params:
//   - ctx: request-scoped context; when already cancelled the handler is disabled.
//   - r: the RecordEvent candidate.
//
// Returns:
//   - bool: true when r.Level is at or above the configured minimum and ctx is live.
func (h *TextHandler) Enabled(ctx context.Context, r corelogger.RecordEvent) bool {
	//: honour context cancellation: cancelled contexts short-circuit to disabled.
	if ctx != nil && ctx.Err() != nil {
		//: caller's context is already done; skip emission entirely.
		return false
	}
	//: threshold check; handlers below min are expected to skip formatting.
	return r.Level >= h.min
}

// WithAttrs returns a derived TextHandler sharing the writer but owning a new
// attrs slice that prepends the given attributes to every record.
//
// Params:
//   - attrs: attributes to prepend on each emitted RecordEvent.
//
// Returns:
//   - corelogger.Handler: a new handler carrying the combined attrs.
func (h *TextHandler) WithAttrs(attrs []corelogger.AttrValue) corelogger.Handler {
	//: copy-on-write — child must not alias the parent's attrs slice.
	cp := make([]corelogger.AttrValue, len(h.attrs)+len(attrs))
	//: preserve ordering: parent attrs first, then the freshly bound ones.
	copy(cp, h.attrs)
	copy(cp[len(h.attrs):], attrs)
	//: share the writer and clock, but own a private attrs slice and mutex.
	return &TextHandler{w: h.w, attrs: cp, groups: h.groups, min: h.min, clk: h.clk}
}

// WithGroup returns a derived TextHandler that namespaces every subsequent
// attribute key under the given group name (rendered as "name.key").
//
// Params:
//   - name: group prefix; empty string yields the receiver unchanged.
//
// Returns:
//   - corelogger.Handler: a new handler carrying the appended group.
func (h *TextHandler) WithGroup(name string) corelogger.Handler {
	//: empty group is a documented no-op so callers can pass user input.
	if name == "" {
		//: return the receiver unchanged — no extra wrapping.
		return h
	}
	//: copy-on-write — child must not alias the parent's groups slice.
	cp := make([]string, len(h.groups)+1)
	copy(cp, h.groups)
	cp[len(h.groups)] = name
	//: share the writer/clock/attrs, own a private groups slice and mutex.
	return &TextHandler{w: h.w, attrs: h.attrs, groups: cp, min: h.min, clk: h.clk}
}

// Handle formats and writes a RecordEvent to the underlying io.Writer.
//
// Params:
//   - ctx: request-scoped context; a cancelled ctx aborts the write.
//   - r: the RecordEvent to render.
//
// Returns:
//   - error: ctx.Err() if cancelled, otherwise whatever the writer returned.
func (h *TextHandler) Handle(ctx context.Context, r corelogger.RecordEvent) error {
	//: honour context cancellation: cancelled contexts skip the write entirely.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so consumers get both our reason and stdlib Is().
		return errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeCtxCancelled,
			Reason:  "CTX_CANCELLED",
			Public:  "Logging aborted due to cancellation",
			Private: "service/logger.TextHandler.Handle saw a cancelled context",
		})
	}
	//: borrow a buffer and render the line via a dedicated helper.
	bp := buffer.Get()
	defer buffer.Put(bp)
	line := h.renderLine(*bp, r)
	*bp = line
	//: delegate the serialised write; helper also wraps any writer error.
	return h.writeLine(line)
}

// renderLine formats a RecordEvent into the provided byte buffer. Extracted
// from Handle to keep both functions below the KTN line-count ceiling.
//
// Params:
//   - b: starting buffer (typically fresh from the kernel pool).
//   - r: the RecordEvent being rendered; Time gets a clock fallback when zero.
//
// Returns:
//   - []byte: the fully formatted line including the trailing newline.
func (h *TextHandler) renderLine(b []byte, r corelogger.RecordEvent) []byte {
	//: fall back to the handler clock when the caller did not timestamp.
	if r.Time.IsZero() {
		//: use injected clock so tests can freeze time deterministically.
		r.Time = h.clk.Now()
	}
	//: render the header in the documented "TIME LEVEL msg" order.
	b = r.Time.AppendFormat(b, timestampLayout)
	b = append(b, ' ')
	b = append(b, r.Level.String()...)
	b = append(b, ' ')
	b = append(b, r.Message...)
	//: handler-bound attrs precede record-bound attrs for consistent output.
	for _, a := range h.attrs {
		//: render each handler attr next to the previous byte contents.
		b = appendAttrWithGroups(b, h.groups, a)
	}
	//: then append any attrs attached directly to the RecordEvent.
	for _, a := range r.Attrs {
		//: render each record-local attr using the same encoding rules.
		b = appendAttrWithGroups(b, h.groups, a)
	}
	//: terminate the line so downstream consumers can split on '\n'.
	return append(b, '\n')
}

// writeLine serialises the mutex-guarded write and wraps any writer error.
//
// Params:
//   - line: the already formatted bytes to emit.
//
// Returns:
//   - error: nil on success; WriteFailed wrapping the writer's error otherwise.
func (h *TextHandler) writeLine(line []byte) error {
	//: serialise writes so concurrent goroutines never interleave lines.
	h.mu.Lock()
	_, werr := h.w.Write(line)
	h.mu.Unlock()
	//: wrap any writer error with the WriteFailed sentinel semantics.
	if werr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the writer's cause.
		return errs.Wrap(werr, errs.WrapParams{
			Code:    CodeWriteFailed,
			Reason:  "WRITE_FAILED",
			Public:  "Log write failed",
			Private: "service/logger.TextHandler.Handle underlying writer returned an error",
		}, errs.Int("bytes", len(line)))
	}
	//: happy path — nothing to report.
	return nil
}

// appendAttrWithGroups prepends the active group prefix stack ("g1.g2.…")
// to the attribute key before delegating the value-rendering to appendAttr.
//
// Params:
//   - dst: buffer to append to; returned grown.
//   - groups: active group prefix stack (outermost first).
//   - a: attribute whose Key and Value are rendered.
//
// Returns:
//   - []byte: the potentially re-sliced buffer after append operations.
func appendAttrWithGroups(dst []byte, groups []string, a corelogger.AttrValue) []byte {
	//: no groups → fall back to the bare appendAttr behaviour.
	if len(groups) == 0 {
		//: skip the prefix construction entirely.
		return appendAttr(dst, a)
	}
	//: write the leading separator + group chain into the buffer.
	dst = append(dst, ' ')
	//: walk the group stack to emit "g1.g2.…" before the attribute key.
	for _, g := range groups {
		//: append each group name followed by the canonical separator.
		dst = append(dst, g...)
		dst = append(dst, groupSeparator)
	}
	//: now append the bare key=value (without the leading space appendAttr writes).
	dst = append(dst, a.Key...)
	dst = append(dst, '=')
	dst = appendValueOnly(dst, a)
	//: hand the (possibly re-allocated) buffer back to the caller.
	return dst
}

// appendValueOnly renders only the value part of an AttrValue; shared by the
// grouped and ungrouped paths so the encoding contract has a single source.
//
// Params:
//   - dst: buffer to append to; returned grown.
//   - a: attribute whose Value is rendered.
//
// Returns:
//   - []byte: the potentially re-sliced buffer after append operations.
func appendValueOnly(dst []byte, a corelogger.AttrValue) []byte {
	//: dispatch on the typed Kind discriminant — same table as appendAttr.
	switch a.Value.Kind() {
	//: strings are quoted so whitespace in values remains visible.
	case corelogger.KindString:
		//: strconv.AppendQuote handles escaping consistently with the bare path.
		return strconv.AppendQuote(dst, a.Value.String())
	//: int64 (also covers int / int32 widened by IntValue) renders base-10.
	case corelogger.KindInt64:
		//: strconv.AppendInt is the canonical alloc-free integer renderer.
		return strconv.AppendInt(dst, a.Value.Int64(), decimalBase)
	//: booleans render as "true" or "false".
	case corelogger.KindBool:
		//: strconv.AppendBool emits the canonical Go spelling.
		return strconv.AppendBool(dst, a.Value.Bool())
	//: floats use Go's default shortest round-trip format.
	case corelogger.KindFloat64:
		//: 'g' format with -1 precision matches Go's default fmt.Print rendering.
		return strconv.AppendFloat(dst, a.Value.Float64(), floatFormat, floatPrec, floatBitSize)
	//: every other Kind degrades to '?' until commit 7's encoder split.
	default:
		//: '?' is the documented placeholder for unsupported variants.
		return append(dst, '?')
	}
}

// appendAttr serialises a single AttrValue onto dst in "key=value" form.
//
// Params:
//   - dst: buffer to append to; returned grown.
//   - a: attribute whose Key and Value are rendered.
//
// Returns:
//   - []byte: the potentially re-sliced buffer after append operations.
func appendAttr(dst []byte, a corelogger.AttrValue) []byte {
	//: separator between message and first attr, and between consecutive attrs.
	dst = append(dst, ' ')
	dst = append(dst, a.Key...)
	dst = append(dst, '=')
	//: dispatch on the typed Kind discriminant — no boxing on the hot path.
	switch a.Value.Kind() {
	//: strings are quoted so whitespace in values remains visible.
	case corelogger.KindString:
		dst = strconv.AppendQuote(dst, a.Value.String())
	//: int64 (also covers int / int32 widened by IntValue) renders base-10.
	case corelogger.KindInt64:
		dst = strconv.AppendInt(dst, a.Value.Int64(), decimalBase)
	//: booleans render as "true" or "false".
	case corelogger.KindBool:
		dst = strconv.AppendBool(dst, a.Value.Bool())
	//: floats use Go's default shortest round-trip format.
	case corelogger.KindFloat64:
		dst = strconv.AppendFloat(dst, a.Value.Float64(), floatFormat, floatPrec, floatBitSize)
	//: every other Kind (Any, Time, Duration, Uint64, Group) maps to '?' for now —
	//: richer rendering ships with the Encoder split in commit 7.
	default:
		dst = append(dst, '?')
	}
	//: return the (possibly re-allocated) buffer back to the caller.
	return dst
}
