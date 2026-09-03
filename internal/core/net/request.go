// Package net — the outbound request description handed to a Policy.
package net

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
