// Package client — the policy-enforcing RoundTripper.
package client

import (
	"net/http"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// guard decorates an http.RoundTripper with the outbound policy, the response
// size ceiling and the observation hook.
//
// Placing the check HERE rather than in the typed client is the whole design.
// A caller cannot construct a request that bypasses it, because there is no
// path to the network that does not pass through the transport — not even for
// code that takes the *http.Client and forges its own request. "Read-only"
// stops being a convention the next contributor must remember and becomes a
// property of the code.
type guard struct {
	// next is the underlying transport.
	next http.RoundTripper
	// policy authorises every request; never nil after construction.
	policy corenet.Policy
	// maxBytes caps the response body.
	maxBytes int64
	// hook observes each call; may be nil.
	hook corenet.CallHook
}

// RoundTrip implements http.RoundTripper.
func (g *guard) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	call := corenet.CallValue{
		Method: req.Method,
		Host:   req.URL.Host,
		Path:   req.URL.EscapedPath(),
	}
	//: the policy is consulted BEFORE the transport is touched, so a refusal
	//: leaves no trace on the network — not even a DNS lookup.
	if derr := g.policy.Allow(requestOf(req)); derr != nil {
		call.Err = derr
		g.observe(call)
		//: return the policy's own refusal unchanged.
		return nil, derr
	}
	started := time.Now()
	resp, err = g.next.RoundTrip(req)
	call.Duration = time.Since(started)
	//: an unreachable peer is reported as a domain failure, and the transport
	//: message is not echoed — it can carry the internal address.
	if err != nil {
		call.Err = err
		g.observe(call)
		//: the transport message is not echoed — it can carry the internal address.
		return nil, errs.Wrap(corenet.CallFailed, errs.WrapParams{},
			errs.String("method", req.Method))
	}
	call.Status = resp.StatusCode
	g.observe(call)
	//: the ceiling is installed on the body handed back, so it holds even for a
	//: caller that reads the response itself through the escape hatch.
	resp.Body = &cappedBody{inner: resp.Body, limit: g.maxBytes}
	//: the response is handed back with its body already bounded.
	return resp, nil
}

// observe hands the completed call to the hook when one is configured.
func (g *guard) observe(call corenet.CallValue) {
	//: the hook is optional; a nil hook costs one comparison per call.
	if g.hook != nil {
		g.hook(call)
	}
}

// requestOf projects an outbound request onto the immutable value a Policy
// judges. The projection is deliberate: a Policy never receives the *http.Request
// itself, so it cannot mutate the request it is authorising.
func requestOf(req *http.Request) corenet.RequestValue {
	//: EscapedPath is the form that goes on the wire; url.URL.Path is already
	//: percent-decoded and would let "%2e%2e" pass a correctly written rule.
	return corenet.RequestValue{
		Method:      req.Method,
		Scheme:      req.URL.Scheme,
		Host:        req.URL.Host,
		EscapedPath: req.URL.EscapedPath(),
		RawQuery:    req.URL.RawQuery,
	}
}
