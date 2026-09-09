// Package net — range 0.2.11.* (ADR 0029 core/net block).
package net

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.11.0 - 0.2.11.255

// CodeListenFailed identifies a listener that could not be bound to its address.
const CodeListenFailed errs.Code = 0x00_02_0B_01 // 0.2.11.1

// CodeInvalidAddress identifies a listen or dial address that is syntactically
// unusable (empty host and port, malformed socket path, negative port).
const CodeInvalidAddress errs.Code = 0x00_02_0B_02 // 0.2.11.2

// CodeUnsupportedNetwork identifies a network name the domain does not serve
// (anything outside tcp, tcp4, tcp6, udp, udp4, udp6, unix, unixgram, unixpacket).
const CodeUnsupportedNetwork errs.Code = 0x00_02_0B_03 // 0.2.11.3

// CodeServerClosed identifies work refused because the server already stopped
// accepting; it is the expected terminal outcome of a graceful shutdown.
const CodeServerClosed errs.Code = 0x00_02_0B_04 // 0.2.11.4

// CodeAlreadyStarted identifies a Start call on a server that is already serving.
const CodeAlreadyStarted errs.Code = 0x00_02_0B_05 // 0.2.11.5

// CodeNotStarted identifies an operation that requires a running server on one
// that has not been started.
const CodeNotStarted errs.Code = 0x00_02_0B_06 // 0.2.11.6

// CodeHandlerMissing identifies a listener group that was bound without a handler.
const CodeHandlerMissing errs.Code = 0x00_02_0B_07 // 0.2.11.7

// CodeHandlerPanic identifies a panic recovered inside a handler; the connection
// is closed but the process survives.
const CodeHandlerPanic errs.Code = 0x00_02_0B_08 // 0.2.11.8

// CodeConnLimitReached identifies a connection rejected because the group's
// concurrency ceiling was saturated.
const CodeConnLimitReached errs.Code = 0x00_02_0B_09 // 0.2.11.9

// CodeDrainTimeout identifies a shutdown whose drain budget expired with
// connections still in flight; the remainder is closed hard.
const CodeDrainTimeout errs.Code = 0x00_02_0B_0A // 0.2.11.10

// CodeGroupUnknown identifies a lookup for a listener group that was never declared.
const CodeGroupUnknown errs.Code = 0x00_02_0B_0B // 0.2.11.11

// CodeGroupDuplicate identifies a second declaration of an already-declared
// listener group name.
const CodeGroupDuplicate errs.Code = 0x00_02_0B_0C // 0.2.11.12

// CodeSocketAdoptFailed identifies an inherited socket that could not be adopted
// from the supervisor.
const CodeSocketAdoptFailed errs.Code = 0x00_02_0B_0D // 0.2.11.13

// CodePacketTooLarge identifies a datagram longer than the group's accepted size.
const CodePacketTooLarge errs.Code = 0x00_02_0B_0E // 0.2.11.14

// CodeTLSMaterialInvalid identifies TLS material that is absent, malformed, or
// yields no usable certificate; it is never downgraded to an empty trust store.
const CodeTLSMaterialInvalid errs.Code = 0x00_02_0B_0F // 0.2.11.15

// CodeTLSHandshakeFailed identifies a TLS or mTLS handshake that did not complete.
const CodeTLSHandshakeFailed errs.Code = 0x00_02_0B_10 // 0.2.11.16

// CodeRequestDenied identifies an outbound request refused by the transport
// policy before it left the process.
const CodeRequestDenied errs.Code = 0x00_02_0B_11 // 0.2.11.17

// CodeResponseTooLarge identifies a response body that exceeded the configured
// read ceiling.
const CodeResponseTooLarge errs.Code = 0x00_02_0B_12 // 0.2.11.18

// CodeCallFailed identifies an outbound call that did not produce a response.
const CodeCallFailed errs.Code = 0x00_02_0B_13 // 0.2.11.19

// CodeTooManyRedirects identifies an outbound call that exceeded its redirect budget.
const CodeTooManyRedirects errs.Code = 0x00_02_0B_14 // 0.2.11.20

// CodeInvalidDuration identifies a configuration duration that is neither a Go
// duration literal nor an integer nanosecond count.
const CodeInvalidDuration errs.Code = 0x00_02_0B_15 // 0.2.11.21

// CodeUnsafePath identifies an outbound path carrying a dot segment, literal or
// percent-encoded. Such a path is refused before any allowlist pattern is tried,
// because an anchored pattern like ^/v1/supi/[^/]+$ happily matches "/v1/supi/.."
// which the upstream then normalises to a different resource.
const CodeUnsafePath errs.Code = 0x00_02_0B_16 // 0.2.11.22

// CodeSSEFieldInvalid identifies a Server-Sent Events frame the wire format
// cannot carry: a line terminator inside an id, an event name or a comment
// (the format has no escape — a newline SPLITS a value, so an id carrying one
// would silently become a different id plus a stray field), a negative retry,
// a retry under one millisecond (the wire field is an integer millisecond
// count, and truncating to zero would say "reconnect immediately"), or a frame
// with no field set at all.
const CodeSSEFieldInvalid errs.Code = 0x00_02_0B_17 // 0.2.11.23

// CodeSSEFlushUnsupported identifies a ResponseWriter that cannot be flushed.
// Server-Sent Events is a streaming format: without a flush every event sits in
// the transport buffer until the handler returns, which for an endless stream
// means the client receives nothing, ever.
const CodeSSEFlushUnsupported errs.Code = 0x00_02_0B_18 // 0.2.11.24

// CodeSSEStreamClosed identifies a send on a stream that has already ended —
// the client disconnected, the server began draining, or the handler closed it.
const CodeSSEStreamClosed errs.Code = 0x00_02_0B_19 // 0.2.11.25

// CodeSSEStreamMisconfigured identifies a stream option the domain refuses to
// interpret rather than guess at: a negative keep-alive interval or a negative
// per-write budget (ADR 0031).
const CodeSSEStreamMisconfigured errs.Code = 0x00_02_0B_1A // 0.2.11.26

// CodeWSHandshakeFailed identifies an HTTP request that is not a valid RFC 6455
// opening handshake: wrong method, absent or non-"websocket" Upgrade token, a
// Connection header without "Upgrade", a Sec-WebSocket-Key that does not decode
// to sixteen bytes, a Sec-WebSocket-Version other than 13, or an Origin the
// server's policy refuses. The response is written before this is returned, so
// the handler has nothing left to answer.
const CodeWSHandshakeFailed errs.Code = 0x00_02_0B_1B // 0.2.11.27

// CodeWSUpgradeUnsupported identifies a ResponseWriter whose connection cannot
// be taken over — an HTTP/2 request, or a middleware wrapper that hides
// http.Hijacker. WebSocket is not a response format: it replaces HTTP on the
// socket, so without the hijack there is nothing to upgrade.
const CodeWSUpgradeUnsupported errs.Code = 0x00_02_0B_1C // 0.2.11.28

// CodeWSProtocolViolation identifies a frame RFC 6455 forbids: an unmasked
// client frame (§5.1), a set reserved bit or reserved opcode (§5.2), a
// fragmented or oversized control frame (§5.5), a non-minimal length encoding
// (§5.2), a continuation with no message in progress or a new data frame
// interrupting one (§5.4), or a malformed close payload (§5.5.1). Each fails
// the connection.
const CodeWSProtocolViolation errs.Code = 0x00_02_0B_1D // 0.2.11.29

// CodeWSMessageTooLarge identifies a frame or an accumulated message beyond the
// connection's configured ceiling. The frame's announced length is checked
// BEFORE any buffer is sized from it, because a 64-bit length field a peer
// chooses is an out-of-memory condition one allocation away.
const CodeWSMessageTooLarge errs.Code = 0x00_02_0B_1E // 0.2.11.30

// CodeWSInvalidPayload identifies a payload the protocol cannot carry: a text
// message or close reason that is not valid UTF-8 (§8.1), a close code that
// must never appear on the wire (1004, 1005, 1006, 1015 and the unallocated
// ranges — §7.4.2), or a close reason past the control-frame ceiling.
const CodeWSInvalidPayload errs.Code = 0x00_02_0B_1F // 0.2.11.31

// CodeWSConnClosed identifies an operation on a connection that has ended — the
// peer sent Close, the server began draining, the handler closed it, or the
// socket died. It is the terminal outcome a receive loop runs until.
const CodeWSConnClosed errs.Code = 0x00_02_0B_20 // 0.2.11.32

// CodeWSConnMisconfigured identifies a connection option the domain refuses to
// interpret rather than guess at: a negative ping interval or write budget, or
// a non-positive size ceiling (ADR 0031).
const CodeWSConnMisconfigured errs.Code = 0x00_02_0B_21 // 0.2.11.33
