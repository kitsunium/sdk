// Package client — the policy-enforcing RoundTripper.
package client

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// stubTransport returns a fixed outcome and records that it was reached.
//
// Whether it was reached at all is the assertion that matters: a refusal must
// leave no trace on the network, and a counter that stays at zero is what
// proves it.
type stubTransport struct {
	// body is the response payload; a nil response is built from it.
	body string
	// status is the response status code.
	status int
	// err is the transport failure, if any.
	err error
	// calls counts how many times the transport was entered.
	calls *int
}

// RoundTrip implements http.RoundTripper.
func (s *stubTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*s.calls++
	//: a configured failure stands in for an unreachable peer.
	if s.err != nil {
		//: nothing came back.
		return nil, s.err
	}
	//: a minimal response is enough; the guard only reads the status and body.
	return &http.Response{
		StatusCode: s.status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

// newGuardRequest builds an outbound request for the guard to judge.
func newGuardRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	return req
}

// Test_guard_RoundTrip is the security guarantee of this package.
//
// The policy is consulted BEFORE the transport is entered, so a refused request
// leaves no trace on the network — not even a DNS lookup. Placing the check
// here rather than in the typed client is what makes it a property of the code:
// there is no path to the network that does not pass through the transport, not
// even for a caller that takes the *http.Client and forges its own request.
func Test_guard_RoundTrip(t *testing.T) {
	t.Parallel()
	denied := errs.Wrap(corenet.RequestDenied, errs.WrapParams{}, errs.String("why", "no"))
	unreachable := errors.New("dial tcp 10.0.0.1:8443: connect: connection refused")

	type tc struct {
		// name describes the case.
		name string
		// policyErr is the verdict the policy returns.
		policyErr error
		// transportErr is the failure the transport reports.
		transportErr error
		// status is the response status when the transport succeeds.
		status int
		// wantReached is whether the transport must have been entered.
		wantReached bool
		// wantCode is the code the caller must receive, or zero for success.
		wantCode errs.Code
		// wantObserved is how many records the hook must have received by the
		// time RoundTrip returns.
		wantObserved int
	}
	tests := []tc{
		{
			//: a refusal never reaches the network.
			name:      "a refused request",
			policyErr: denied, wantCode: corenet.CodeRequestDenied, wantObserved: 1,
		},
		{
			name:         "an unreachable peer",
			transportErr: unreachable,
			wantReached:  true, wantCode: corenet.CodeCallFailed, wantObserved: 1,
		},
		{
			//: the record is deferred to the body, which is the only place a size
			//: is known — so nothing is observed yet when RoundTrip returns.
			name:   "an admitted request",
			status: http.StatusOK, wantReached: true,
		},
		{
			name:   "an admitted request that fails upstream",
			status: http.StatusBadGateway, wantReached: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var reached, consulted int
		var observed []corenet.CallValue
		g := &guard{
			next:     &stubTransport{status: c.status, body: "hello", err: c.transportErr, calls: &reached},
			policy:   stubPolicy{err: c.policyErr, calls: &consulted},
			maxBytes: 1024,
			hook:     func(call corenet.CallValue) { observed = append(observed, call) },
		}
		req := newGuardRequest(t, http.MethodGet, "https://sdm:8443/v1/subscribers")

		resp, err := g.RoundTrip(req)

		//: the policy is consulted on every single request.
		if consulted != 1 {
			t.Fatalf("the policy was consulted %d times, want 1", consulted)
		}
		if (reached > 0) != c.wantReached {
			t.Fatalf("the transport was entered %d times, want reached = %v", reached, c.wantReached)
		}
		if len(observed) != c.wantObserved {
			t.Fatalf("the hook fired %d times, want %d", len(observed), c.wantObserved)
		}
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("RoundTrip = %v, want code %v", err, c.wantCode)
			}
			//: a refused or failed call is recorded, without a status it never got.
			if observed[0].Err == nil {
				t.Error("the record carries no error for a call that failed")
			}
			if observed[0].Status != 0 {
				t.Errorf("a failed call reported status %d, want none", observed[0].Status)
			}
			//: the transport's own text names the internal address, so it is not
			//: echoed to the caller.
			if c.transportErr != nil && strings.Contains(err.Error(), "10.0.0.1") {
				t.Errorf("the refusal echoes the internal address: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("RoundTrip = %v, want nil", err)
		}
		//: the ceiling is installed on the body handed back, so it holds even for
		//: a caller reading the response itself through the escape hatch.
		capped, ok := resp.Body.(*cappedBody)
		if !ok {
			t.Fatalf("the body is a %T, want the bounded wrapper", resp.Body)
		}
		if capped.limit != g.maxBytes {
			t.Errorf("the body ceiling is %d, want %d", capped.limit, g.maxBytes)
		}
		//: reading the body to its end is what emits the record.
		if _, rerr := io.ReadAll(resp.Body); rerr != nil {
			t.Fatalf("reading the body: %v", rerr)
		}
		if len(observed) != 1 {
			t.Fatalf("the hook fired %d times once the body ended, want 1", len(observed))
		}
		if observed[0].Status != c.status {
			t.Errorf("the record carries status %d, want %d", observed[0].Status, c.status)
		}
		if observed[0].Bytes != int64(len("hello")) {
			t.Errorf("the record carries %d bytes, want %d", observed[0].Bytes, len("hello"))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_guard_observe pins that the hook is OPTIONAL. Observation is a
// deployment concern: a caller that wants no audit trail passes nil, and that
// must cost a comparison rather than a panic on the first call.
func Test_guard_observe(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// withHook installs an observer.
		withHook bool
		// call is the record handed to observe.
		call corenet.CallValue
	}
	tests := []tc{
		{name: "no hook at all", call: corenet.CallValue{Method: "GET"}},
		{
			name:     "a successful call",
			withHook: true,
			call:     corenet.CallValue{Method: "GET", Host: "sdm:8443", Path: "/v1/x", Status: 200},
		},
		{
			name:     "an invalid call",
			withHook: true,
			call:     corenet.CallValue{Method: "GET", Err: errors.New("denied")},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var observed []corenet.CallValue
		g := &guard{}
		if c.withHook {
			g.hook = func(call corenet.CallValue) { observed = append(observed, call) }
		}

		g.observe(c.call)

		if !c.withHook {
			//: reaching here at all is the assertion: a nil hook must not panic.
			if len(observed) != 0 {
				t.Fatalf("a guard with no hook observed %d calls", len(observed))
			}
			return
		}
		if len(observed) != 1 {
			t.Fatalf("the hook fired %d times, want 1", len(observed))
		}
		//: the record reaches the hook unchanged; the guard does not edit it.
		if observed[0] != c.call {
			t.Errorf("the hook saw %+v, want %+v", observed[0], c.call)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_requestOf pins the two properties that make a Policy safe to write.
//
// It receives a VALUE, never the *http.Request, so it cannot mutate the request
// it is authorising — a policy that rewrote the path would be authorising one
// request and sending another. And the path it judges is the ESCAPED form, the
// one that goes on the wire: url.URL.Path is already percent-decoded, so a rule
// written against "%2e%2e" would never fire on the request that carries it.
func Test_requestOf(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// target is the request URL.
		target string
		// wantPath is the escaped path the policy must be shown.
		wantPath string
		// wantQuery is the raw query the policy must be shown.
		wantQuery string
	}
	tests := []tc{
		{name: "a plain path", target: "https://sdm:8443/v1/subscribers", wantPath: "/v1/subscribers"},
		{
			//: the decoded Path would read "/v1/.." and slip past a rule written
			//: against the wire form.
			name:   "an encoded dot segment stays encoded",
			target: "https://sdm:8443/v1/%2e%2e", wantPath: "/v1/%2e%2e",
		},
		{
			name:   "an encoded separator stays encoded",
			target: "https://sdm:8443/v1/supi/a%2fb", wantPath: "/v1/supi/a%2fb",
		},
		{
			name:   "a query is carried through",
			target: "https://sdm:8443/v1/x?a=b+c&d=e", wantPath: "/v1/x", wantQuery: "a=b+c&d=e",
		},
		{name: "an empty path", target: "https://sdm:8443", wantPath: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		req := newGuardRequest(t, http.MethodGet, c.target)
		parsed, perr := url.Parse(c.target)
		if perr != nil {
			t.Fatalf("parsing %q: %v", c.target, perr)
		}

		got := requestOf(req, req.URL.EscapedPath())

		if got.Method != http.MethodGet {
			t.Errorf("Method = %q, want GET", got.Method)
		}
		if got.Scheme != parsed.Scheme || got.Host != parsed.Host {
			t.Errorf("origin = %s://%s, want %s://%s", got.Scheme, got.Host, parsed.Scheme, parsed.Host)
		}
		//: the wire form, not the decoded one.
		if got.EscapedPath != c.wantPath {
			t.Errorf("EscapedPath = %q, want %q", got.EscapedPath, c.wantPath)
		}
		if got.RawQuery != c.wantQuery {
			t.Errorf("RawQuery = %q, want %q", got.RawQuery, c.wantQuery)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_guard_handsThePolicyTheEscapedPath pins the ONE thing that could go
// wrong in reading url.URL.EscapedPath once instead of twice.
//
// RoundTrip used to call it separately for the observation record and for the
// value the policy judges. It now reads it once and passes it to both, which
// costs one scan and one allocation less on a percent-encoded path — and puts a
// plain string parameter where a method call used to be. A later contributor
// passing req.URL.Path instead compiles, reads fine, and silently hands every
// policy the DECODED path: `%2e%2e` becomes `..`, and an allowlist written
// against the wire form stops matching what is on the wire.
//
// The record the hook receives is checked with it, because it is the same
// string and a regression would take both.
//
// MUTATION: passing req.URL.Path to requestOf in RoundTrip fails with
// `the policy judged "/v1/../a%2fb", want "/v1/%2e%2e/a%2Fb"` — the decoded
// form, in which the dot segment is a literal ".." no encoded-form rule fires
// on and the encoded separator has become a real one.
func Test_guard_handsThePolicyTheEscapedPath(t *testing.T) {
	t.Parallel()
	const target string = "https://sdm:8443/v1/%2e%2e/a%2Fb"
	const want string = "/v1/%2e%2e/a%2Fb"
	var judged string
	var observed []corenet.CallValue
	g := &guard{
		next:   &stubTransport{status: http.StatusOK, body: "ok", calls: new(0)},
		policy: corenet.PolicyFunc(func(req corenet.RequestValue) error { judged = req.EscapedPath; return nil }),
		hook:   func(call corenet.CallValue) { observed = append(observed, call) },
	}
	req := newGuardRequest(t, http.MethodGet, target)
	//: the fixture is only meaningful if url.URL kept a distinct raw form.
	if req.URL.RawPath == "" || req.URL.RawPath == req.URL.Path {
		t.Fatalf("the fixture needs a URL whose escaped and decoded paths differ; RawPath = %q, Path = %q",
			req.URL.RawPath, req.URL.Path)
	}

	resp, err := g.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip = %v, want nil", err)
	}
	//: the policy must judge the form that goes on the wire.
	if judged != want {
		t.Errorf("the policy judged %q, want %q", judged, want)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatalf("closing the body: %v", cerr)
	}
	if len(observed) != 1 {
		t.Fatalf("the hook fired %d times, want 1", len(observed))
	}
	//: and the record must carry the same string, not the decoded one.
	if observed[0].Path != want {
		t.Errorf("the record carries path %q, want %q", observed[0].Path, want)
	}
}
