// Package slogbridge — the slog.Handler that forwards records to an SDK Logger.
package slogbridge

import (
	"context"
	"log/slog"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// handler forwards every slog record to an SDK Logger.
//
// It is immutable: WithAttrs and WithGroup derive a new handler, so a parent
// never observes a child's bindings. That is the slog.Handler contract, and it
// falls out naturally because the SDK Logger's With is copy-on-write too.
type handler struct {
	// lg is the destination. Attrs bound through WithAttrs are folded into it,
	// so a derived handler carries its own derived Logger.
	lg logger.Logger
	// prefix is the active group chain ("g1.g2"), applied to every key this
	// handler qualifies.
	//
	// The chain is tracked HERE rather than delegated to lg.WithGroup because
	// the two group models disagree: the SDK handler applies its final group
	// stack to every bound attr, so an attr bound BEFORE a group would be
	// retroactively moved under it. slog's model is positional — a group only
	// governs what is bound after it. Qualifying keys in the bridge reproduces
	// slog's semantics exactly, and does so independently of which SDK handler
	// or encoder sits underneath.
	prefix string
}

// Enabled reports whether the underlying Logger would emit a record at lv.
//
// The SDK Logger's threshold is the only one consulted: the bridge holds no
// level of its own, which is what makes "one pipeline, one threshold" true.
func (h handler) Enabled(ctx context.Context, lv slog.Level) bool {
	//: delegate so a LevelVar retuned at runtime takes effect here too.
	return h.lg.Enabled(ctx, toLevel(lv))
}

// Handle converts r's attributes and emits the record through the Logger.
//
// It always returns nil: the SDK Logger.Log contract reports no error, so the
// bridge has none to forward. Sink-level failures are handled by the sink's own
// middleware (failover / recover), not surfaced through slog.
func (h handler) Handle(ctx context.Context, r slog.Record) error {
	//: size the slice from the record's own count; groups may grow it further,
	//: but this is the right starting capacity for the common flat case.
	attrs := make([]logger.Attr, 0, r.NumAttrs())
	//: walk the record's attrs; the callback keeps slog's iteration contract.
	r.Attrs(func(a slog.Attr) bool {
		//: qualify under the active group chain and convert.
		attrs = appendAttr(attrs, h.prefix, a)
		//: never stop early — every attr belongs in the record.
		return true
	})
	//: r.Time is deliberately dropped: Logger.Log takes no timestamp, so the
	//: SDK handler stamps the record from its own clock (see the package doc).
	h.lg.Log(ctx, toLevel(r.Level), r.Message, attrs...)
	//: the SDK emission path reports no error to forward.
	return nil
}

// WithAttrs returns a handler whose records always carry as.
func (h handler) WithAttrs(as []slog.Attr) slog.Handler {
	//: slog contract: an empty slice is a no-op, so avoid deriving a Logger.
	if len(as) == 0 {
		//: return the receiver unchanged — nothing to bind.
		return h
	}
	//: convert under the CURRENT chain so these attrs freeze the group they
	//: were bound in, even if a later WithGroup extends the chain.
	attrs := make([]logger.Attr, 0, len(as))
	//: fold each attr, flattening any group it carries.
	for _, a := range as {
		//: qualify under the active chain and convert.
		attrs = appendAttr(attrs, h.prefix, a)
	}
	//: elision can empty the slice (all-zero attrs); binding nothing is a no-op.
	if len(attrs) == 0 {
		//: return the receiver unchanged rather than an equivalent copy.
		return h
	}
	//: keys are already fully qualified, so the derived Logger needs no group
	//: state of its own — this is what keeps the two group models from mixing.
	return handler{lg: h.lg.With(attrs...), prefix: h.prefix}
}

// WithGroup returns a handler that namespaces subsequent keys under name.
func (h handler) WithGroup(name string) slog.Handler {
	//: slog contract: an empty name is a no-op so callers can pass user input.
	if name == "" {
		//: return the receiver unchanged — no empty segment in the chain.
		return h
	}
	//: extend the chain only; the Logger is shared untouched, so attrs already
	//: bound on it keep the qualification they were given.
	return handler{lg: h.lg, prefix: qualifyGroup(h.prefix, name)}
}
