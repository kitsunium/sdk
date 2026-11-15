package logger

import (
	"context"
	"slices"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/buffer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
)

// genericHandler is the default corelogger.Handler implementation. It runs
// records through the configured Encoder and forwards the resulting bytes
// to the configured Sink.
type genericHandler struct {
	// enc renders RecordEvent payloads into bytes.
	enc encoder.Encoder
	// sink delivers the encoder's bytes to the underlying transport.
	sink corelogger.Sink
	// attrs are prepended to every RecordEvent (handler-bound With*).
	attrs []corelogger.AttrValue
	// penc is enc as a corelogger.PrefixEncoder, nil when it is not one: the
	// bound attributes are then rendered with the record's on every emit.
	penc corelogger.PrefixEncoder
	// prefix is attrs rendered once under groups by penc (#1016), spliced into
	// every line; nil with no bound attribute or no PrefixEncoder. WithAttrs
	// and WithGroup render it again, since the group stack prefixes every key.
	prefix []byte
	// groups is the active group prefix stack accumulated via WithGroup.
	groups []string
	// sinkAttrs is the sink's answer to corelogger.AttrsRequester, asked once
	// at construction: false hands it the record's own attributes, unmerged
	// and uncopied, since it reads only the bytes (#1016).
	sinkAttrs bool
	// floor is the minimum level the handler emits, read on every record so a
	// level.Var retuned with Set takes effect on the next one (#1015); a
	// handler built from a plain Level holds level.Fixed. Derived handlers
	// share it, so one Set reaches every logger derived from this one.
	floor level.Leveler
	// clk sources the timestamp when RecordEvent.Time is the zero value. The
	// handler owns the Time fill so the encoded line and any downstream sink
	// share one coherent instant (V25).
	clk clock.Clock
}

// NewHandler composes enc with sink and gates emission on the supplied
// minimum level. A nil enc OR sink is rejected so callers cannot
// accidentally construct a dead handler.
func NewHandler(enc encoder.Encoder, sink corelogger.Sink, min level.Level) (h corelogger.Handler, err error) {
	//: a constant floor: the same gate a live one is, through one interface call.
	return NewHandlerLeveled(enc, sink, level.Fixed(min))
}

// NewHandlerLeveled is NewHandler gated on a live floor: Enabled reads
// floor.Level on every record, so a level.Var retuned with Set takes effect on
// the next record, for this handler and every handler derived from it with
// WithAttrs or WithGroup. A nil floor gates at level.Info, the zero
// MinLevel. A nil enc OR sink is rejected as NewHandler rejects it.
func NewHandlerLeveled(enc encoder.Encoder, sink corelogger.Sink, floor level.Leveler) (h corelogger.Handler, err error) {
	//: reject nil encoder so the format step never panics on the hot path.
	if enc == nil {
		//: caller supplied no encoder — return the documented sentinel.
		return nil, corelogger.EncoderNil
	}
	//: reject nil sink so the transport step never panics on the hot path.
	if sink == nil {
		//: caller supplied no sink — return the documented sentinel.
		return nil, corelogger.SinkRequired
	}
	//: an encoder that renders bound attributes once is kept as one (#1016).
	penc, _ := enc.(corelogger.PrefixEncoder)
	//: hand back the configured handler bound to the real wall clock.
	return &genericHandler{enc: enc, penc: penc, sink: sink, sinkAttrs: corelogger.SinkWantsAttrs(sink), floor: floorOrInfo(floor), clk: clock.System}, nil
}

// floorOrInfo is floor, or the constant Info floor for a nil one — the zero
// MinLevel's floor, so a nil Leveler is the default and not a panic.
func floorOrInfo(floor level.Leveler) level.Leveler {
	//: the documented default.
	if floor == nil {
		return level.Fixed(level.Info)
	}
	//: the caller's floor, live or constant.
	return floor
}

// Enabled reports whether the handler would emit a record at r.Level, taking
// context cancellation into account.
func (h *genericHandler) Enabled(ctx context.Context, r corelogger.RecordEvent) bool {
	//: honour context cancellation: cancelled contexts short-circuit to disabled.
	if ctx != nil && ctx.Err() != nil {
		//: caller's context is already done; skip emission entirely.
		return false
	}
	//: threshold check against the floor's current value (#1015).
	return r.Level >= h.floor.Level()
}

// Handle encodes r through the configured encoder and forwards the bytes to
// the configured sink. Borrows a scratch buffer from kernel/concur/buffer so the
// ENCODING step itself does not allocate; the attrs the sink is handed are a
// separate cost — one slice for a record with attributes of its own, none
// for a record without them or a sink that reads only the bytes (#1016, see
// pkg/v1/observe/logger/BENCH.md).
func (h *genericHandler) Handle(ctx context.Context, r corelogger.RecordEvent) error {
	//: honour context cancellation early so the encoder step never runs.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so consumers get both our reason and stdlib Is().
		return errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    corelogger.CodeCtxCancelled,
			Reason:  "CTX_CANCELLED",
			Public:  "Logging aborted due to cancellation",
			Private: "service/observe/logger.genericHandler.Handle saw a cancelled context",
		})
	}
	//: stamp Time once at the handler boundary when the caller left it zero so
	//: the encoded line and any downstream sink observe one coherent instant
	//: (V25). Sinks MUST NOT restamp a non-zero Time.
	if r.Time.IsZero() {
		//: source the instant from the injected clock so tests can freeze it.
		r.Time = h.clk.Now()
	}
	//: borrow a buffer so the encoder writes into a recycled scratchpad.
	bp := buffer.Get()
	defer buffer.Put(bp)
	//: format the record into the borrowed buffer, then hand the sink the
	//: record with the bound attrs prepended — route predicates and the memory
	//: sink read them — or its own alone, for a sink that reads none.
	line, merged := h.encode(*bp, r)
	*bp = line
	r.Attrs = merged
	//: hand the encoded bytes to the sink for delivery.
	_, werr := h.sink.Write(ctx, r, line)
	//: return the sink's error verbatim (already wrapped by the sink).
	return werr
}

// encode renders r into dst and returns the line and the attributes the sink
// is handed. With a PrefixEncoder the bound attributes are the prefix rendered
// when they were bound, and only r's own are rendered here (#1016); otherwise
// the encoder renders the merged set. The sink is handed the bound attributes
// first, then r's — or r's alone when it reads none (corelogger.AttrsRequester).
func (h *genericHandler) encode(dst []byte, r corelogger.RecordEvent) (line []byte, attrs []corelogger.AttrValue) {
	//: bound attributes not rendered ahead of time: the encoder renders them.
	if h.prefix == nil && len(h.attrs) > 0 {
		attrs = h.merge(r.Attrs)
		r.Attrs = attrs
		return h.enc.Append(dst, h.groups, r), attrs
	}
	line = h.render(dst, r)
	//: a sink that reads only the bytes is spared the merge.
	if !h.sinkAttrs {
		return line, r.Attrs
	}
	return line, h.merge(r.Attrs)
}

// render writes r with the bound attributes h holds as a prefix, if any:
// either nothing is bound or the encoder rendered it when it was.
func (h *genericHandler) render(dst []byte, r corelogger.RecordEvent) []byte {
	//: nothing bound: the record alone.
	if h.prefix == nil {
		return h.enc.Append(dst, h.groups, r)
	}
	//: the prefix, then the record's own attributes.
	return h.penc.AppendWithPrefix(dst, h.groups, h.prefix, r)
}

// merge returns the bound attributes followed by own. A record with none of
// its own shares h.attrs, clipped to its length so a sink's append
// reallocates rather than writing past it: the array is immutable once
// bound (WithAttrs copies it) and the record is read-only (corelogger.Sink),
// so the copy — 64 bytes an attribute — buys nothing (#1016). Otherwise it is
// the one allocation of an emit.
func (h *genericHandler) merge(own []corelogger.AttrValue) []corelogger.AttrValue {
	//: nothing to append to the bound set.
	if len(own) == 0 {
		return slices.Clip(h.attrs)
	}
	return mergeAttrs(h.attrs, own)
}

// derive returns a child of h bound to attrs under groups, its prefix
// rendered once for the pair when the encoder is a PrefixEncoder.
func (h *genericHandler) derive(attrs []corelogger.AttrValue, groups []string) *genericHandler {
	//: share encoder/sink/floor/clk; attrs, groups and prefix are the child's.
	child := &genericHandler{enc: h.enc, penc: h.penc, sink: h.sink, sinkAttrs: h.sinkAttrs, attrs: attrs, groups: groups, floor: h.floor, clk: h.clk}
	//: render once what every record of the child would render again.
	if h.penc != nil && len(attrs) > 0 {
		child.prefix = h.penc.AppendAttrs(nil, groups, attrs)
	}
	return child
}

// WithAttrs returns a derived genericHandler with the supplied attributes
// prepended onto every subsequent RecordEvent.
func (h *genericHandler) WithAttrs(attrs []corelogger.AttrValue) corelogger.Handler {
	//: copy-on-write — child must not alias the parent's attrs slice.
	cp := mergeAttrs(h.attrs, attrs)
	//: the child owns a private attrs slice and the prefix rendered from it.
	return h.derive(cp, h.groups)
}

// WithGroup returns a derived genericHandler with the supplied group name
// appended onto the active group prefix stack.
func (h *genericHandler) WithGroup(name string) corelogger.Handler {
	//: empty group is a documented no-op so callers can pass user input.
	if name == "" {
		//: return the receiver unchanged — no extra wrapping.
		return h
	}
	//: the child, its bound attrs rendered again under the longer stack.
	return h.withGroup(name)
}

// withGroup is WithGroup's derivation, kept out of it so WithGroup stays
// within the inlining budget: the child owns a private groups slice
// (copy-on-write — it must not alias the parent's) and the prefix rendered
// under it.
func (h *genericHandler) withGroup(name string) *genericHandler {
	cp := make([]string, len(h.groups)+1)
	copy(cp, h.groups)
	cp[len(h.groups)] = name
	//: the bound attrs are the parent's; the stack and the prefix are new.
	return h.derive(h.attrs, cp)
}

// mergeAttrs returns a fresh slice combining parent and child attributes
// in order (parent first). Extracted so WithAttrs and Handle share a
// single allocation contract.
func mergeAttrs(parent, child []corelogger.AttrValue) []corelogger.AttrValue {
	//: short-circuit when the parent prefix is empty — clone child verbatim.
	if len(parent) == 0 {
		//: defensive copy of child so the caller cannot mutate handler state.
		return slices.Clone(child)
	}
	//: build the combined slice via slices.Concat for one canonical allocation.
	return slices.Concat(parent, child)
}
