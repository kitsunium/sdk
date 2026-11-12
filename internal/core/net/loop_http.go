package net

import corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"

// LoopHTTPValue is what a group tells a LoopHTTPCodec at Start: the versions
// its Protocols option names, nil when it set none, and whether the
// operator switched http2-loop off.
type LoopHTTPValue struct {
	// Protocols is the group's Protocols list, nil when the group gave none.
	Protocols []string
	// HTTP2Off is set when KITSUNIUM_NET_DISABLE switched http2-loop off.
	HTTP2Off bool
	// HTTP2 is the HTTP2 option's bounds, the zero value when the group gave none.
	HTTP2 corehttp2.LimitsValue
}

// HTTPCountsValue is what a LoopHTTPCounter reports: the requests a loop
// adapter served per version and the HTTP/2 errors it counted, the numbers
// ListenerStateValue carries under the same names.
type HTTPCountsValue struct {
	// HTTP1Requests counts the HTTP/1.1 requests served.
	HTTP1Requests uint64
	// HTTP2Requests counts the HTTP/2 requests served.
	HTTP2Requests uint64
	// HTTP2Errors counts the HTTP/2 errors by type, a row for each counted.
	HTTP2Errors []corehttp2.ErrorCountValue
}
