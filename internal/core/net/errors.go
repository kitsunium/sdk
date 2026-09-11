// Package net — declares the sentinel *errs.Error outcomes of the network
// domain. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
// Service implementations and the pkg/v1 facades wrap these sentinels; they
// declare no codes of their own (the ADR 0016 proc precedent).
package net

import "github.com/kitsunium/sdk/internal/kernel/errs"

// sysexits codes restated locally so the sentinels below carry an honest process
// exit status without importing a platform header.
const (
	exitUsage       int = 64 // EX_USAGE — the caller supplied something unusable
	exitDataErr     int = 65 // EX_DATAERR — the peer supplied something unusable
	exitUnavailable int = 69 // EX_UNAVAILABLE — the service could not be reached or served
	exitSoftware    int = 70 // EX_SOFTWARE — an internal invariant broke
	exitOSErr       int = 71 // EX_OSERR — an OS facility refused us
	exitTempFail    int = 75 // EX_TEMPFAIL — transient; retrying later may succeed
	exitNoPerm      int = 77 // EX_NOPERM — the action was refused by policy
	exitConfig      int = 78 // EX_CONFIG — the configuration itself is wrong
)

var (
	// ListenFailed is returned when a listener could not be bound.
	ListenFailed = errs.Define(CodeListenFailed, "LISTEN_FAILED",
		"The server could not bind its listening address",
		"core/net: binding a listener failed; the wrap cause is the OS error",
		errs.WithExitCode(exitUnavailable))

	// InvalidAddress is returned for a syntactically unusable address.
	InvalidAddress = errs.Define(CodeInvalidAddress, "INVALID_ADDRESS",
		"The network address is not valid",
		"core/net: the supplied host:port or socket path could not be parsed",
		errs.WithExitCode(exitUsage))

	// UnsupportedNetwork is returned for a network name outside the served set.
	UnsupportedNetwork = errs.Define(CodeUnsupportedNetwork, "UNSUPPORTED_NETWORK",
		"The network type is not supported",
		"core/net: network outside {tcp,tcp4,tcp6,udp,udp4,udp6,unix,unixgram,unixpacket}",
		errs.WithExitCode(exitUsage))

	// ServerClosed is returned once the server has stopped accepting work.
	ServerClosed = errs.Define(CodeServerClosed, "SERVER_CLOSED",
		"The server is closed",
		"core/net: the server stopped accepting; this is the terminal state of a graceful shutdown",
		errs.WithExitCode(exitUnavailable))

	// AlreadyStarted is returned by Start on a server that is already serving.
	AlreadyStarted = errs.Define(CodeAlreadyStarted, "ALREADY_STARTED",
		"The server is already started",
		"core/net: Start called on a server whose phase is already Serving",
		errs.WithExitCode(exitSoftware))

	// NotStarted is returned when an operation requires a running server.
	NotStarted = errs.Define(CodeNotStarted, "NOT_STARTED",
		"The server is not started",
		"core/net: the operation requires a running server",
		errs.WithExitCode(exitSoftware))

	// HandlerMissing is returned when a group was bound without a handler.
	HandlerMissing = errs.Define(CodeHandlerMissing, "HANDLER_MISSING",
		"The listener group has no handler",
		"core/net: a group reached Start with a nil handler",
		errs.WithExitCode(exitUsage))

	// HandlerPanic wraps a panic recovered inside a handler.
	HandlerPanic = errs.Define(CodeHandlerPanic, "HANDLER_PANIC",
		"The connection handler failed unexpectedly",
		"core/net: a handler panicked; the connection was closed and the process kept running",
		errs.WithExitCode(exitSoftware))

	// ConnLimitReached is returned when the group's connection ceiling is full.
	ConnLimitReached = errs.Define(CodeConnLimitReached, "CONN_LIMIT_REACHED",
		"The server is at its connection limit",
		"core/net: the group's concurrency ceiling rejected the connection",
		errs.WithExitCode(exitTempFail))

	// DrainTimeout is returned when the shutdown drain budget expired.
	DrainTimeout = errs.Define(CodeDrainTimeout, "DRAIN_TIMEOUT",
		"The server did not drain before its deadline",
		"core/net: connections were still in flight when the drain budget expired",
		errs.WithExitCode(exitTempFail))

	// GroupUnknown is returned for a lookup of an undeclared group.
	GroupUnknown = errs.Define(CodeGroupUnknown, "GROUP_UNKNOWN",
		"The listener group is not declared",
		"core/net: no group is registered under that name",
		errs.WithExitCode(exitUsage))

	// GroupDuplicate is returned for a second declaration of one group name.
	GroupDuplicate = errs.Define(CodeGroupDuplicate, "GROUP_DUPLICATE",
		"The listener group is already declared",
		"core/net: a group with that name already exists on this server",
		errs.WithExitCode(exitUsage))

	// SocketAdoptFailed is returned when an inherited socket could not be adopted.
	SocketAdoptFailed = errs.Define(CodeSocketAdoptFailed, "SOCKET_ADOPT_FAILED",
		"An inherited socket could not be adopted",
		"core/net: a supervisor-passed descriptor could not be turned into a listener",
		errs.WithExitCode(exitOSErr))

	// PacketTooLarge names the reason a datagram beyond the group's accepted
	// size is dropped. It is deliberately not returned to anyone: a read loop
	// has no caller, so the drop is reported through
	// StateValue.OversizedPackets. The sentinel stays because it is what gives
	// that count a documented name in the registry.
	PacketTooLarge = errs.Define(CodePacketTooLarge, "PACKET_TOO_LARGE",
		"The datagram is larger than the accepted size",
		"core/net: the received datagram exceeded the group's maximum packet size",
		errs.WithExitCode(exitDataErr))

	// TLSMaterialInvalid is returned for absent, malformed, or empty TLS material.
	// It is deliberately loud: an unusable bundle must never degrade into a trust
	// store that silently verifies nothing.
	TLSMaterialInvalid = errs.Define(CodeTLSMaterialInvalid, "TLS_MATERIAL_INVALID",
		"The TLS material is missing or invalid",
		"core/net: the certificate, key, or CA bundle yielded no usable material",
		errs.WithExitCode(exitConfig))

	// TLSHandshakeFailed is returned when a TLS or mTLS handshake did not complete.
	TLSHandshakeFailed = errs.Define(CodeTLSHandshakeFailed, "TLS_HANDSHAKE_FAILED",
		"The TLS handshake failed",
		"core/net: the TLS or mTLS handshake did not complete",
		errs.WithExitCode(exitUnavailable))

	// RequestDenied is returned when the transport policy refused an outbound call.
	// The public message never names the refused path, so a denial cannot leak the
	// shape of the private API surface through a log line.
	RequestDenied = errs.Define(CodeRequestDenied, "REQUEST_DENIED",
		"The request was refused by policy",
		"core/net: the transport policy refused the outbound request",
		errs.WithExitCode(exitNoPerm))

	// ResponseTooLarge is returned when a response body exceeded the read ceiling.
	ResponseTooLarge = errs.Define(CodeResponseTooLarge, "RESPONSE_TOO_LARGE",
		"The response is larger than the accepted size",
		"core/net: the response body exceeded the configured maximum size",
		errs.WithExitCode(exitDataErr))

	// CallFailed is returned when an outbound call produced no response.
	CallFailed = errs.Define(CodeCallFailed, "CALL_FAILED",
		"The outbound call failed",
		"core/net: the outbound call did not produce a response",
		errs.WithExitCode(exitUnavailable))

	// TooManyRedirects is returned when a call exceeded its redirect budget.
	TooManyRedirects = errs.Define(CodeTooManyRedirects, "TOO_MANY_REDIRECTS",
		"The outbound call exceeded its redirect budget",
		"core/net: the redirect chain was longer than the configured maximum",
		errs.WithExitCode(exitUnavailable))

	// InvalidDuration is returned for a configuration duration that parses as
	// neither a Go duration literal nor an integer nanosecond count.
	InvalidDuration = errs.Define(CodeInvalidDuration, "INVALID_DURATION",
		"The duration value is not valid",
		"core/net: the value is neither a Go duration literal nor a nanosecond count",
		errs.WithExitCode(exitConfig))

	// UnsafePath is returned for a path carrying a dot segment in any form. It is
	// evaluated before the allowlist, because Go normalises dot segments neither
	// in url.URL nor in the transport, so an anchored pattern alone is not enough.
	UnsafePath = errs.Define(CodeUnsafePath, "UNSAFE_PATH",
		"The request path is not in normalised form",
		"core/net: the path carries a literal or percent-encoded dot segment",
		errs.WithExitCode(exitNoPerm))

	// SSEFieldInvalid is returned for a Server-Sent Events frame the wire format
	// cannot carry. The format has no escape mechanism: a newline inside a value
	// SPLITS it rather than being quoted, so an id or an event name carrying one
	// would arrive as a different value plus a stray field. It is refused, never
	// truncated — a silently shortened id is a resume token that points at the
	// wrong place.
	SSEFieldInvalid = errs.Define(CodeSSEFieldInvalid, "SSE_FIELD_INVALID",
		"The event cannot be represented in the event-stream format",
		"core/net: an SSE frame carried a line terminator in a single-line field, an unrepresentable retry, or no field at all",
		errs.WithExitCode(exitUsage))

	// SSEFlushUnsupported is returned when the ResponseWriter cannot be flushed.
	// Without a flush the events accumulate in the transport buffer and reach the
	// client only when the handler returns, so an endless stream delivers nothing
	// at all — the failure is total and silent, which is why it is refused at
	// construction rather than discovered in production.
	SSEFlushUnsupported = errs.Define(CodeSSEFlushUnsupported, "SSE_FLUSH_UNSUPPORTED",
		"The response cannot be streamed",
		"core/net: the http.ResponseWriter implements neither Flush nor http.Flusher",
		errs.WithExitCode(exitSoftware))

	// SSEStreamClosed is returned by a send on a stream that has already ended:
	// the client disconnected, the server began draining, or the handler closed
	// it. It is the terminal outcome a streaming handler loops until.
	SSEStreamClosed = errs.Define(CodeSSEStreamClosed, "SSE_STREAM_CLOSED",
		"The event stream is closed",
		"core/net: the SSE stream ended — peer gone, server draining, or closed by the handler",
		errs.WithExitCode(exitUnavailable))

	// SSEStreamMisconfigured is returned for a stream option the domain refuses
	// to interpret rather than guess at (ADR 0031). A zero keep-alive interval is
	// CLAMPED to the domain default, because a working keep-alive needs no
	// explanation; a negative one is REFUSED, because no value the SDK invented
	// for it would be defensible and "never" already has an explicit spelling.
	SSEStreamMisconfigured = errs.Define(CodeSSEStreamMisconfigured, "SSE_STREAM_MISCONFIGURED",
		"The event stream options are not valid",
		"core/net: a negative keep-alive interval or write budget was supplied",
		errs.WithExitCode(exitConfig))
)
