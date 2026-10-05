//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/net/client .

// Package client is the outbound half of the SDK's network domain (ADR 0029):
// an HTTP client whose posture is enforced by the transport rather than by the
// call site.
//
// # Why the policy lives in the transport
//
// A [Policy] is evaluated inside the http.RoundTripper, underneath every call
// site. A caller cannot construct a request that skips it, because there is no
// path to the network that does not pass through the transport. "This client is
// read-only" therefore stops being a convention the next contributor has to
// remember and becomes a property of the code — which is why [Client].HTTP can
// safely hand the underlying *http.Client to a third-party SDK without
// forfeiting the guarantee.
//
// Every default is closed. A conjunction with no members refuses, a path
// allowlist with no patterns refuses, and a client built with a nil policy is
// refused outright. An allowlist that lost its contents — a renamed config key,
// a slice never populated — must close, never quietly become a passthrough.
//
// # Building a read-only client
//
//	allow, err := client.AllowPaths(`/v1/(subscribers|supi/[^/]+|profiles(/[^/]+)?)`)
//	deny, err := client.DenyPaths(`/v1/(authentication|policy|eir|oam).*`)
//	policy := client.Policies(client.AllowMethods("GET"), deny, allow)
//
//	c, err := client.New(client.Config{BaseURL: "https://sdm.core.svc:8000"},
//		identity, policy, func(call client.CallInfo) {
//			logger.Info(ctx, lg, "egress",
//				logger.String("path", call.Path), logger.Int("status", call.Status))
//		})
//
//	resp, err := c.Get(ctx, "/v1/subscribers", url.Values{"profileId": {"P-tenant-a"}})
//
// Patterns are supplied UNANCHORED and anchored by the constructor. Leaving
// anchoring to the caller is exactly the omission that turns an allowlist into a
// sieve, and it is invisible in review.
//
// # What the response gives you
//
// Get and Do return a fully-read [Response] rather than an *http.Response. A
// returned *http.Response makes the caller responsible for closing the body and
// bounding its size, and one forgotten Close leaks a connection. It also makes
// an honest byte count impossible, since the size of a body is only known once
// it has been read.
//
// A non-2xx status returns BOTH the response and an error: a caller diagnosing
// the failure needs the body, and a caller who never considered the status still
// gets an error. A 204 with no body is a normal outcome, not a failure.
//
// # What it deliberately does not do
//
// It does not decode. Depending on the codec registry would link every codec
// the SDK ships into every consumer that only wanted a guarded GET, so
// decoding belongs above this layer.
//
// It does not truncate an oversized body — it fails with [ResponseTooLarge]. A
// silent truncation does not stay silent; it resurfaces several layers away as
// an incomprehensible decode error on a body that looks complete.
package client
