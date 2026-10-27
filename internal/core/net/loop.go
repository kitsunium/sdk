package net

// LoopConnRef names one connection of a loop: the ring it lives on, its slot
// in that ring's table and the generation of that slot. It is a comparable value
// with no behaviour, so a handler can key a map with it and hold it without
// holding anything of the engine.
//
// A slot is reused only once every operation of its previous generation has
// completed, and the generation then moves on, so an operation on a reference
// whose connection closed does nothing and returns LoopStaleRef — it can never
// reach the connection that took the slot since. The zero value names no
// connection.
type LoopConnRef struct {
	// Ring is the ring, inside its loop, that holds the connection.
	Ring uint32
	// Slot is the connection's index in the ring's table.
	Slot uint32
	// Gen is the slot's generation. It is never zero for a live connection, so
	// the zero LoopConnRef is stale on every loop.
	Gen uint32
}

// PostFunc is an operation posted to a loop with LoopIO.Post. It runs once, on
// the loop's thread, with the loop's LoopIO and the integer it was posted with.
// It is a plain function rather than a closure so the queue holds no pointer the
// poster built: a top-level function and an index into the caller's own table
// post without allocating.
type PostFunc func(io LoopIO, arg uintptr)

// OptionKind names a group option, for the one question a loop group asks of
// its options at Start: is this one honoured (LoopOptionHonourer)? It names
// options, not their values.
type OptionKind uint8

// The group options a loop group judges at Start, by kind.
const (
	// OptionNone names no option; it is the zero value.
	OptionNone OptionKind = iota
	// OptionTLS is TLS. A loop group always refuses it, with LoopTLSUnsupported.
	OptionTLS
	// OptionMiddleware is Use. A loop group always refuses it: there is no
	// ConnHandler to decorate.
	OptionMiddleware
	// OptionReadTimeout is ReadTimeout.
	OptionReadTimeout
	// OptionWriteTimeout is WriteTimeout.
	OptionWriteTimeout
	// OptionIdleTimeout is IdleTimeout. The loop engine honours it.
	OptionIdleTimeout
	// OptionReadHeaderTimeout is ReadHeaderTimeout, which only a protocol adapter
	// can honour.
	OptionReadHeaderTimeout
	// OptionMaxHeaderBytes is MaxHeaderBytes, which only a protocol adapter can
	// honour.
	OptionMaxHeaderBytes
	// OptionHandshakeTimeout is HandshakeTimeout.
	OptionHandshakeTimeout
	// OptionReadBufferSize is ReadBufferSize.
	OptionReadBufferSize
	// OptionMaxPacketSize is MaxPacketSize.
	OptionMaxPacketSize
	// OptionBatchSize is BatchSize.
	OptionBatchSize
	// OptionShards is Shards.
	OptionShards
	// OptionMaxConns is MaxConns. The loop engine honours it, refusing with
	// ConnLimitReached.
	OptionMaxConns
	// OptionAdopt is Adopt. The loop engine honours it.
	OptionAdopt
	// OptionPreHandshake is PreHandshake.
	OptionPreHandshake
	// OptionPlaintext is Plaintext. Every engine honours it; with TLS it is refused
	// with PlaintextWithTLS.
	OptionPlaintext
	// OptionName is Name. Every engine honours it.
	OptionName
	// OptionEngine is Engine. Every engine honours it, or refuses the group with
	// EngineUnsupported.
	OptionEngine
	// OptionLoopMiddleware is UseLoop. The loop engine honours it; every group no
	// loop handler serves refuses it with LoopOptionUnsupported.
	OptionLoopMiddleware
	// OptionBatchReplies is BatchReplies. A datagram group honours it; a stream
	// group, goroutine or loop, refuses it with LoopOptionUnsupported (ADR 0192).
	OptionBatchReplies
)

// optionNames holds each kind's constructor name, indexed by kind. Its length
// is pinned to the constant block by Test_OptionKind_String, so the two cannot fall
// out of step silently.
var optionNames = [...]string{
	OptionNone:              "none",
	OptionTLS:               "TLS",
	OptionMiddleware:        "Use",
	OptionReadTimeout:       "ReadTimeout",
	OptionWriteTimeout:      "WriteTimeout",
	OptionIdleTimeout:       "IdleTimeout",
	OptionReadHeaderTimeout: "ReadHeaderTimeout",
	OptionMaxHeaderBytes:    "MaxHeaderBytes",
	OptionHandshakeTimeout:  "HandshakeTimeout",
	OptionReadBufferSize:    "ReadBufferSize",
	OptionMaxPacketSize:     "MaxPacketSize",
	OptionBatchSize:         "BatchSize",
	OptionShards:            "Shards",
	OptionMaxConns:          "MaxConns",
	OptionAdopt:             "Adopt",
	OptionPreHandshake:      "PreHandshake",
	OptionPlaintext:         "Plaintext",
	OptionName:              "Name",
	OptionEngine:            "Engine",
	OptionLoopMiddleware:    "UseLoop",
	OptionBatchReplies:      "BatchReplies",
}

// String renders the option's name, as its constructor spells it.
func (k OptionKind) String() string {
	//: a kind past the table is reported rather than indexed out of range.
	if int(k) >= len(optionNames) {
		//: the out-of-range value is named, never hidden.
		return "unknown"
	}
	//: the constructor's own spelling, which is what an error field names.
	return optionNames[k]
}
