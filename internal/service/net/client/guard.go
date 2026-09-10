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
	//: EscapedPath is computed ONCE and carried. It is needed twice — by the
	//: observation record and by the value the policy judges — and it is not a
	//: field read: on a percent-encoded path, the one the safety checks exist to
	//: catch, url.URL re-validates RawPath and unescapes, which allocates. Two
	//: calls made the adversarial input cost two allocations instead of one.
	escaped := req.URL.EscapedPath()
	//: the policy is consulted BEFORE the transport is touched, so a refusal
	//: leaves no trace on the network — not even a DNS lookup.
	if derr := g.policy.Allow(requestOf(req, escaped)); derr != nil {
		//: a refusal has no body to wait for, so it is reported immediately.
		g.observe(corenet.CallValue{
			Method: req.Method,
			Host:   req.URL.Host,
			Path:   escaped,
			Err:    derr,
		})
		//: return the policy's own refusal unchanged.
		return nil, derr
	}
	started := time.Now()
	resp, err = g.next.RoundTrip(req)
	elapsed := time.Since(started)
	//: an unreachable peer is reported as a domain failure, and the transport
	//: message is not echoed — it can carry the internal address.
	if err != nil {
		//: an unreachable peer has no body to wait for either.
		g.observe(corenet.CallValue{
			Method:   req.Method,
			Host:     req.URL.Host,
			Path:     escaped,
			Duration: elapsed,
			Err:      err,
		})
		//: the transport message is not echoed — it can carry the internal address.
		return nil, errs.Wrap(corenet.CallFailed, errs.WrapParams{},
			errs.String("method", req.Method))
	}
	//: the ceiling is installed on the body handed back, so it holds even for a
	//: caller that reads the response itself through the escape hatch. It is
	//: installed UNCONDITIONALLY: there is no unbounded mode, and an over-sized
	//: body fails rather than arriving truncated.
	capped := &cappedBody{inner: resp.Body, limit: g.maxBytes}
	//: observation is installed WITH the body rather than fired here. A body's
	//: size is only known once it has been read, so a record emitted now could
	//: never carry one — CallValue.Bytes was documented and permanently zero.
	//: And an over-sized body or a failed close happens strictly after this
	//: point, so firing here recorded both as clean successes. The body reports
	//: itself exactly once, on whichever of EOF, a read failure or Close comes
	//: first, which observes the escape hatch on the same terms as Do.
	//
	//: the record and the closure that fills it are built only when there is a
	//: hook to receive them. That is the whole reason this is a branch rather
	//: than an unconditional assignment: a closure writing into the record
	//: forces the record onto the heap, so installing both regardless made the
	//: DEFAULT configuration — a nil hook — pay two heap allocations per
	//: request for an observer that does not exist.
	if g.hook != nil {
		call := corenet.CallValue{
			Method:   req.Method,
			Host:     req.URL.Host,
			Path:     escaped,
			Status:   resp.StatusCode,
			Duration: elapsed,
		}
		capped.done = func(read int64, cerr error) {
			call.Bytes = read
			call.Err = cerr
			g.observe(call)
		}
	}
	resp.Body = capped
	//: the response is handed back with its body already bounded.
	return resp, nil
}

// observe hands the completed call to the hook when one is configured.
//
// A nil hook costs one comparison HERE, and nothing at all on the request path:
// the CallValue is passed by value, so a refusal reported to no observer never
// leaves the stack, and RoundTrip builds neither the success record nor the
// closure that fills it unless g.hook is set. The comparison alone was once the
// whole claim, and it was wrong about the request even while being right about
// this function.
func (g *guard) observe(call corenet.CallValue) {
	//: the hook is optional; a nil hook costs one comparison per call.
	if g.hook != nil {
		g.hook(call)
	}
}

// requestOf projects an outbound request onto the immutable value a Policy
// judges. The projection is deliberate: a Policy never receives the *http.Request
// itself, so it cannot mutate the request it is authorising.
//
// escapedPath is passed in rather than read from req because the caller already
// needs it for the observation record, and url.URL.EscapedPath is not a field
// read — see RoundTrip. It MUST be req.URL.EscapedPath() and never the decoded
// url.URL.Path, which would let "%2e%2e" pass a correctly written rule.
func requestOf(req *http.Request, escapedPath string) corenet.RequestValue {
	//: the escaped form is what goes on the wire, so it is what a policy judges.
	return corenet.RequestValue{
		Method:      req.Method,
		Scheme:      req.URL.Scheme,
		Host:        req.URL.Host,
		EscapedPath: escapedPath,
		RawQuery:    req.URL.RawQuery,
	}
}
