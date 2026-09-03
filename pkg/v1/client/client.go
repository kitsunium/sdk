//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/client .

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
// remember and becomes a property of the code — which is why [Client.HTTP] can
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
// It does not decode. Depending on the codec registry would pull mongo-driver,
// msgpack and cbor into the module graph of every consumer that only wanted a
// guarded GET, so decoding belongs above this layer.
//
// It does not truncate an oversized body — it fails with [ResponseTooLarge]. A
// silent truncation does not stay silent; it resurfaces several layers away as
// an incomprehensible decode error on a body that looks complete.
package client

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	svcclient "github.com/kitsunium/sdk/internal/service/net/client"
	"github.com/kitsunium/sdk/pkg/v1/tlsid"
)

// Client performs guarded outbound HTTP calls.
type Client = svcclient.Client

// Config describes an outbound client. Durations accept "5s" as well as a raw
// nanosecond count, so a configuration file stays readable.
type Config = corenet.ClientConfig

// Response is a fully-read outbound response.
type Response = corenet.ResponseValue

// Policy authorises an outbound request. Implementations run inside the
// transport, so no call site can bypass them.
type Policy = corenet.Policy

// PolicyFunc adapts a plain function to Policy.
type PolicyFunc = corenet.PolicyFunc

// RequestInfo is the immutable request description a Policy judges. It carries
// the ESCAPED path: url.URL.Path is already percent-decoded, so a policy that
// judged it would accept "%2e%2e", which the upstream reinterprets as "..".
type RequestInfo = corenet.RequestValue

// CallInfo records one completed outbound call.
type CallInfo = corenet.CallValue

// CallHook receives one CallInfo per outbound call. It must not block.
type CallHook = corenet.CallHook

// Duration is a configuration-friendly time.Duration.
type Duration = corenet.DurationValue

// Sentinels returned by this package. Match with errors.Is or errs.HasCode.
var (
	// RequestDenied reports a request refused by the policy. Its public message
	// never names the refused path, so a denial cannot leak the private API
	// surface into a log line.
	RequestDenied = corenet.RequestDenied
	// UnsafePath reports a path carrying a dot segment, literal or
	// percent-encoded, or a path that is not absolute.
	UnsafePath = corenet.UnsafePath
	// ResponseTooLarge reports a body beyond the configured ceiling.
	ResponseTooLarge = corenet.ResponseTooLarge
	// CallFailed reports an unreachable upstream or a non-2xx status.
	CallFailed = corenet.CallFailed
	// TooManyRedirects reports a redirect chain beyond the configured cap.
	TooManyRedirects = corenet.TooManyRedirects
)

// New builds a guarded client. A nil policy is refused rather than defaulted to
// "allow everything": an unguarded egress path wearing the name of a guarded one
// is worse than no client at all.
func New(cfg Config, id tlsid.Identity, policy Policy, hook CallHook) (c *Client, err error) {
	//: the service layer owns the wiring; this facade only forwards.
	return svcclient.New(cfg, id, policy, hook)
}

// AllowMethods admits only the named HTTP methods. Passing none admits none.
func AllowMethods(methods ...string) Policy {
	//: an empty allowlist closes, which is the safe direction.
	return svcclient.AllowMethods(methods...)
}

// AllowPaths admits only paths matching one of the patterns. The patterns are
// supplied unanchored and anchored here, so no caller can forget to.
func AllowPaths(patterns ...string) (policy Policy, err error) {
	//: a compilation failure surfaces now, not on the first request.
	return svcclient.AllowPaths(patterns...)
}

// DenyPaths refuses paths matching one of the patterns. A deny is final and is
// not overridable by an allow pattern.
func DenyPaths(patterns ...string) (policy Policy, err error) {
	//: a compilation failure surfaces now, not on the first request.
	return svcclient.DenyPaths(patterns...)
}

// Policies requires every policy to allow the request. Composition is by
// conjunction, so adding a policy can only ever narrow what is permitted.
func Policies(members ...Policy) Policy {
	//: an empty conjunction refuses.
	return svcclient.Policies(members...)
}
