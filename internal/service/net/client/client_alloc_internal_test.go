//go:build !race

// Package client — the allocation contracts this package's comments assert.
//
// Two of them were written down and then contradicted by the code. guard.go
// said "the hook is optional; a nil hook costs one comparison per call", which
// was true of observe and false of the request: the CallValue and the closure
// that fills it were installed on EVERY response, so the documented default
// configuration paid two heap allocations per call for an observer that does
// not exist. And path.go lowercased the whole path and every segment to compare
// them against six ASCII constants, which allocated on any path carrying an
// uppercase byte — including the uppercase hex url.URL.EscapedPath itself
// emits, so the checks allocated most on exactly the inputs they exist to
// refuse.
//
// Both are now properties of the code, and both are gated here.
//
// The `!race` constraint is not a preference: the race detector allocates
// shadow state on every memory access, so AllocsPerRun under `-race` measures
// the detector. That makes this file invisible to the race suite, which is why
// //internal/service/net/client:client_test carries an entry in
// tools/alloc-lane-targets.txt — the race-off alloc lane is its ONLY gate
// (SDK-wide rule 12).
package client

import (
	"io"
	"net/http"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// allocRuns is how many times AllocsPerRun exercises each claim. It is well
// past the point where a per-call allocation would round to zero: a single
// malloc on the path reports 1.0, not 1/allocRuns.
const allocRuns int = 500

// allocUppercasePath is an ordinary API path carrying a canonical UUID. Nothing
// about it is adversarial — it is what a real consumer sends — and its
// uppercase hex is what made every path check allocate.
const allocUppercasePath string = "/v1/users/9F8E7D6C-1234-4ABC-9DEF-0123456789AB/orders"

// allocBody is a response body that ends immediately and closes for free, so
// what AllocsPerRun sees on a round trip is the guard's own work.
type allocBody struct{}

// Read implements io.Reader with an immediate EOF.
func (allocBody) Read([]byte) (n int, err error) {
	//: an empty body ends on its first read.
	return 0, io.EOF
}

// Close implements io.Closer.
func (allocBody) Close() error {
	//: there is nothing to release.
	return nil
}

// allocTransport hands back one pre-built response with a fresh body each time,
// allocating nothing itself.
type allocTransport struct {
	// resp is returned by every RoundTrip, re-armed with a body each call.
	resp *http.Response
}

// RoundTrip implements http.RoundTripper without any I/O.
func (t *allocTransport) RoundTrip(*http.Request) (resp *http.Response, err error) {
	t.resp.Body = allocBody{}
	//: the same response value, re-armed.
	return t.resp, nil
}

// allocGuard assembles a guard over the stub transport with the given hook.
func allocGuard(hook corenet.CallHook) *guard {
	return &guard{
		next:     &allocTransport{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}},
		policy:   corenet.PolicyFunc(func(corenet.RequestValue) error { return nil }),
		maxBytes: defaultMaxResponseSize,
		hook:     hook,
	}
}

// TestRoundTripAllocatesOnlyTheCeilingWithoutAHook is the guard behind guard.go's
// claim that a nil hook is free.
//
// One allocation is correct and is the cappedBody: the response ceiling is
// installed UNCONDITIONALLY and must be, because there is no unbounded mode and
// an over-sized body fails rather than arriving truncated. What must NOT be
// there is the observation record and the closure that fills it — the closure
// writes into the record, which forces the record onto the heap, so the two
// arrive together or not at all.
//
// The with-hook arm is measured beside it rather than left implicit, because
// "the default is free" is only interesting next to what observation costs.
//
// MUTATION: replacing RoundTrip's `if g.hook != nil` with `if true` fails at
// `a round trip with no hook allocated 3 times, want 1` — the 3 being the
// cappedBody, the record and the closure, which is exactly what the allocation
// profile attributed to guard.RoundTrip before this change: 92.66 % of the
// benchmark's objects, split 27.8 % on the record, 27.8 % on the cappedBody and
// 37.1 % on the closure. Restoring the whole original function body produces
// the same line.
func TestRoundTripAllocatesOnlyTheCeilingWithoutAHook(t *testing.T) {
	roundTrip := func(g *guard, req *http.Request) func() {
		return func() {
			resp, err := g.RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip: %v", err)
			}
			if cerr := resp.Body.Close(); cerr != nil {
				t.Fatalf("close: %v", cerr)
			}
		}
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"https://api.example.test"+allocUppercasePath, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	//: the default configuration — no observer anywhere.
	if got := testing.AllocsPerRun(allocRuns, roundTrip(allocGuard(nil), req)); got != 1 {
		t.Errorf("a round trip with no hook allocated %v times, want 1", got)
	}
	//: and what an observer costs, stated rather than hidden.
	if got := testing.AllocsPerRun(allocRuns, roundTrip(allocGuard(func(corenet.CallValue) {}), req)); got != 3 {
		t.Errorf("a round trip with a hook allocated %v times, want 3", got)
	}
}

// TestRefusedRoundTripAllocatesNothingBeyondTheRefusal pins the other half of
// the same claim: a request the policy refuses must not build an observation
// record either.
//
// The policy hands back a refusal built ONCE, outside the measurement, which
// is what isolates the guard from errs: the typed refusal and its fields are a
// real and deliberate cost — they are what lets a caller route on
// errs.HasCode — but they are the POLICY's, not this function's. With them held
// still, the guard's own refusal path is free.
//
// MUTATION: restoring the original function body — the CallValue declared at
// the top and a closure at the bottom writing into it, which is what forced it
// onto the heap — fails at `a refused round trip allocated 1 times, want 0`.
// That one is the record no observer was ever going to read, built before the
// policy had even spoken.
func TestRefusedRoundTripAllocatesNothingBeyondTheRefusal(t *testing.T) {
	refusal := errs.Wrap(corenet.RequestDenied, errs.WrapParams{},
		errs.String("why", "path not in the allow list"))
	g := allocGuard(nil)
	g.policy = corenet.PolicyFunc(func(corenet.RequestValue) error { return refusal })
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"https://api.example.test"+allocUppercasePath, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}

	got := testing.AllocsPerRun(allocRuns, func() {
		if _, rerr := g.RoundTrip(req); rerr == nil {
			t.Fatal("the policy admitted a request it was told to refuse")
		}
	})

	//: nothing at all: EscapedPath returns its input for a path with no
	//: percent-encoding, requestOf is a stack value, and no record is built.
	if got != 0 {
		t.Errorf("a refused round trip allocated %v times, want 0", got)
	}
}

// TestPathChecksAllocateNothingOnAnAdmittedPath is the guard behind path.go's
// in-place scans.
//
// The admitted path is the one that runs on every request a policy lets
// through, so it is the one whose cost is paid a million times a day. It
// carries an uppercase UUID because that is the input the ToLower spelling
// allocated on while looking entirely innocent.
//
// The composed shape is measured too, because that is what a consumer actually
// writes: Policies(AllowMethods, DenyPaths, AllowPaths) runs checkPath once per
// PATH policy, so what was one allocation in isolation was four per request.
//
// MUTATION: restoring foldsASCII to `strings.ToLower(s) == want` fails at
// `checkPath allocated 6 times on an admitted path, want 0` and
// `the composed policy allocated 12 times on an admitted path, want 0`. Six,
// not one, because folding inside the comparison lowercases the segment once
// per dotSegments entry, where the original spelling folded once per segment
// and cost two per checkPath. Either way the gate bites — and the exact
// doubling from six to twelve is the point the composed row exists to make,
// since checkPath runs once per PATH policy.
func TestPathChecksAllocateNothingOnAnAdmittedPath(t *testing.T) {
	//: the scans in isolation.
	if got := testing.AllocsPerRun(allocRuns, func() {
		if err := checkPath(allocUppercasePath); err != nil {
			t.Fatalf("checkPath refused an admissible path: %v", err)
		}
	}); got != 0 {
		t.Errorf("checkPath allocated %v times on an admitted path, want 0", got)
	}

	allow, aerr := AllowPaths("/v1/users/[^/]+/orders")
	if aerr != nil {
		t.Fatalf("compiling the allow list: %v", aerr)
	}
	deny, derr := DenyPaths("/v1/users/[^/]+/secrets")
	if derr != nil {
		t.Fatalf("compiling the deny list: %v", derr)
	}
	policy := Policies(AllowMethods(http.MethodGet), deny, allow)
	req := corenet.RequestValue{
		Method:      http.MethodGet,
		Scheme:      "https",
		Host:        "api.example.test",
		EscapedPath: allocUppercasePath,
	}

	//: and the shape a consumer is told to compose, where they run twice.
	if got := testing.AllocsPerRun(allocRuns, func() {
		if err := policy.Allow(req); err != nil {
			t.Fatalf("the composed policy refused an admissible request: %v", err)
		}
	}); got != 0 {
		t.Errorf("the composed policy allocated %v times on an admitted path, want 0", got)
	}
}
