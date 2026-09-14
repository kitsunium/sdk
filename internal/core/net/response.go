// Package net — the completed outbound response value.
package net

import "net/http"

// ResponseValue is a fully-read outbound response.
//
// The client returns this rather than an *http.Response on purpose. A returned
// *http.Response makes the caller responsible for closing the body and for
// reading it under a size cap, and a single forgotten Close leaks a connection.
// It also makes an accurate byte count impossible: the size of a body is only
// known once it has been read, so an observation hook firing at header time can
// never report it. Reading and closing inside the client fixes both.
type ResponseValue struct {
	// Status is the HTTP status code. It is set even when the call also returns
	// an error, so a caller can tolerate a specific status deliberately.
	Status int
	// Header is the response header set.
	Header http.Header
	// Body is the fully-read response body, already bounded by the client's
	// size cap. It is nil for a status that carries no body, such as 204.
	Body []byte
}

// RequestValue describes an outbound request to the transport policy.
//
// Two field choices are load-bearing and were paid for in a real integration:
//
//   - The path is the ESCAPED form, the one that goes on the wire. url.URL.Path
//     is already percent-DECODED, so a policy that judges it accepts "%2e%2e"
//     — which the upstream then reinterprets as "..". Judging the decoded form
//     makes a correctly written allowlist bypassable, so this type does not
//     carry it.
//   - RawQuery is present because a policy often needs to require a parameter,
//     not just permit a path. A read-only policy that cannot see the query
//     cannot, for instance, insist that a listing call passes the filter that
//     makes its result correct.
//
// The value is immutable by construction: a policy receives a copy and cannot
// mutate the request it is judging.
type RequestValue struct {
	// Method is the uppercase HTTP method, e.g. "GET".
	Method string
	// Scheme is the URL scheme, e.g. "https".
	Scheme string
	// Host is the authority, host[:port]. It is carried because a policy that
	// sees only the path cannot defend against a redirect that keeps the path
	// and swaps the origin.
	Host string
	// EscapedPath is the percent-encoded path exactly as it will be sent. It is
	// never the decoded form — see the type comment.
	EscapedPath string
	// RawQuery is the encoded query string, without the leading "?".
	RawQuery string
}
