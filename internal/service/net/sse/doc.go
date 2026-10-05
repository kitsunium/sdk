// Package sse — an event's wire form, as the WHATWG HTML standard's
// Server-Sent Events format spells it: the encoder that turns a
// corenet.SSEEventValue into the bytes a stream writes, and a comment into the
// one frame no client dispatches.
//
// The value and the rules it must satisfy are the core's
// (corenet.SSEEventValue.Validate); writing it is a mechanism, and it moved
// here from internal/core/net (ADR 0160 §4).
//
// Package sse — the stream's functional options and their defaults.
//
// Package sse is the server side of Server-Sent Events (ADR 0029): a
// unidirectional server→client stream over an ordinary HTTP response, carrying
// text/event-stream frames until either end decides it is over.
//
// It is written against net/http's own interfaces, not against this SDK's
// listener engine, so it works on any http.Handler. Mounted on the SDK engine
// it additionally observes the server's drain signal, which is what stops an
// endless stream from holding a graceful shutdown open for its whole budget.
package sse
