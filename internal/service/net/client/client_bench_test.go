// Package client — what the guard, the path checks and the policies cost.
//
// Everything here runs against a STUB transport. pkg/v1/client/BENCH.md already
// prices the end-to-end call against a loopback origin and attributes 5.53 % of
// its allocations to guard.RoundTrip; this file measures what is INSIDE that
// share, which a benchmark carrying a real socket cannot see.
package client

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// benchPlainPath is the ordinary case: lowercase, no percent-encoding, four
	// segments. It is the path against which "the checks are free" would be
	// true even before this change.
	benchPlainPath string = "/v1/tenants/acme/profiles"
	// benchUUIDPath is the case a real consumer actually sends. The identifier
	// is a canonical uppercase UUID, which is what made strings.ToLower
	// allocate on a path carrying nothing adversarial at all.
	benchUUIDPath string = "/v1/users/9F8E7D6C-1234-4ABC-9DEF-0123456789AB/orders"
	// benchEncodedPath carries the percent-encoded separator the checks exist
	// to refuse, in the UPPERCASE hex url.URL.EscapedPath actually emits.
	benchEncodedPath string = "/v1/tenants/acme%2Fevil/profiles"
	// benchDotPath carries a percent-encoded dot segment, uppercase.
	benchDotPath string = "/v1/tenants/%2E%2E/profiles"
	// benchOrigin is the authority every benchmarked request is sent to.
	benchOrigin string = "https://api.example.test"
	// benchTinyBody is the ten-byte reply a lying peer answers with.
	benchTinyBody int = 10
	// benchDeepSegments is how many uppercase segments the depth benchmark
	// walks. It is well past a real API route, which is the point: it shows the
	// per-segment cost is paid per segment and not per path.
	benchDeepSegments int = 8
)

// benchAllowAll admits every request, so a benchmark of the guard measures the
// guard and not a policy's opinion.
var benchAllowAll corenet.Policy = corenet.PolicyFunc(func(corenet.RequestValue) error {
	return nil
})

// benchDeepPath is a path with far more segments than a typical API route,
// every one of them carrying an uppercase byte.
var benchDeepPath string = "/" + strings.Repeat("9F8E7D6C1234/", benchDeepSegments) + "leaf"

// benchString keeps a string result observable so the compiler cannot elide the
// work that produced it.
var benchString string

// benchBool keeps a scan's verdict observable, for the same reason.
var benchBool bool

// benchErr keeps a check's verdict observable, for the same reason.
var benchErr error

// benchBody is a reusable empty response body: it reports EOF at once and its
// Close is a no-op, so the stub transport contributes no allocation of its own
// and what -benchmem reports is the guard's.
type benchBody struct{}

// Read implements io.Reader with an immediate EOF.
func (benchBody) Read([]byte) (n int, err error) {
	//: an empty body ends on its first read.
	return 0, io.EOF
}

// Close implements io.Closer.
func (benchBody) Close() error {
	//: there is nothing to release.
	return nil
}

// benchReader serves a fixed payload and rewinds on every Close, so a body-size
// benchmark allocates its payload once rather than once per iteration.
type benchReader struct {
	// payload is the body handed back, re-served from the start on each open.
	payload []byte
	// off is the read cursor within payload.
	off int
}

// Read implements io.Reader over the remaining payload.
func (r *benchReader) Read(p []byte) (n int, err error) {
	//: the payload is exhausted.
	if r.off >= len(r.payload) {
		//: report the end exactly as a transport body would.
		return 0, io.EOF
	}
	n = copy(p, r.payload[r.off:])
	r.off += n
	//: hand back what fitted in the caller's buffer.
	return n, nil
}

// Close implements io.Closer and rewinds for the next iteration.
func (r *benchReader) Close() error {
	r.off = 0
	//: the payload is ready to be served again.
	return nil
}

// benchTransport hands back one response per call with a fresh body installed,
// so the guard's wrapper never wraps a wrapper left over from the last
// iteration. It touches no socket.
type benchTransport struct {
	// resp is the response returned by every RoundTrip.
	resp *http.Response
	// body is re-installed on resp before each hand-back.
	body io.ReadCloser
}

// RoundTrip implements http.RoundTripper without any I/O.
func (t *benchTransport) RoundTrip(*http.Request) (resp *http.Response, err error) {
	t.resp.Body = t.body
	//: the same response value, re-armed.
	return t.resp, nil
}

// newBenchTransport builds a stub transport serving body.
func newBenchTransport(body io.ReadCloser) *benchTransport {
	return &benchTransport{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
		},
		body: body,
	}
}

// benchRequest builds one outbound request, reused across iterations because
// RoundTrip does not mutate it.
func benchRequest(b *testing.B, path string) *http.Request {
	b.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, benchOrigin+path, nil)
	if err != nil {
		b.Fatalf("building the request: %v", err)
	}
	return req
}

// benchValue projects a path onto the value a Policy judges.
func benchValue(path string) corenet.RequestValue {
	return corenet.RequestValue{
		Method:      http.MethodGet,
		Scheme:      "https",
		Host:        "api.example.test",
		EscapedPath: path,
	}
}

// benchGuardRow prices one guarded round trip. The guard and the request are
// arguments rather than captures so nothing is forced onto the heap by the
// closure itself.
func benchGuardRow(g *guard, req *http.Request) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := g.RoundTrip(req)
			if err != nil {
				b.Fatalf("round trip: %v", err)
			}
			if cerr := resp.Body.Close(); cerr != nil {
				b.Fatalf("close: %v", cerr)
			}
		}
	}
}

// BenchmarkGuardRoundTrip prices one guarded round trip against a stub
// transport, with and without an observation hook.
//
// The two arms are the lead: the hook is optional and nil by default, but the
// closure that reports the body and the CallValue it writes into used to be
// installed unconditionally, so the default configuration paid for a feature it
// does not use.
func BenchmarkGuardRoundTrip(b *testing.B) {
	arms := []struct {
		// name distinguishes the hook configuration.
		name string
		// hook is the observer, nil for the default configuration.
		hook corenet.CallHook
	}{
		{name: "NilHook", hook: nil},
		{name: "WithHook", hook: func(corenet.CallValue) {}},
	}
	for _, arm := range arms {
		g := &guard{
			next:     newBenchTransport(benchBody{}),
			policy:   benchAllowAll,
			maxBytes: defaultMaxResponseSize,
			hook:     arm.hook,
		}
		b.Run(arm.name, benchGuardRow(g, benchRequest(b, benchPlainPath)))
	}
}

// BenchmarkGuardRoundTripEncoded prices the same round trip on a path carrying
// a percent-encoding, which is what makes url.URL.EscapedPath allocate rather
// than return its input — and therefore what a second call to it costs.
func BenchmarkGuardRoundTripEncoded(b *testing.B) {
	g := &guard{
		next:     newBenchTransport(benchBody{}),
		policy:   benchAllowAll,
		maxBytes: defaultMaxResponseSize,
	}
	req := benchRequest(b, benchEncodedPath)
	if req.URL.RawPath == "" {
		b.Fatalf("the benchmark needs a URL whose RawPath is set; got %q", req.URL.RawPath)
	}
	benchGuardRow(g, req)(b)
}

// benchCheckPathRow prices the safety checks on one path shape.
func benchCheckPathRow(path string) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			benchErr = checkPath(path)
		}
	}
}

// BenchmarkCheckPath prices the safety checks that run before any pattern, on
// the four shapes a real path takes.
//
// Plain is the floor. UUID is the ordinary case that is NOT adversarial and
// used to allocate anyway, because EscapedPath emits uppercase hex and a
// canonical UUID is uppercase. Encoded and Dot are the inputs the checks exist
// to refuse, and their remaining allocations are the typed errs refusal.
func BenchmarkCheckPath(b *testing.B) {
	arms := []struct {
		// name describes the path shape.
		name string
		// path is the escaped path handed to checkPath.
		path string
	}{
		{name: "Plain", path: benchPlainPath},
		{name: "UUID", path: benchUUIDPath},
		{name: "EncodedSeparator", path: benchEncodedPath},
		{name: "EncodedDot", path: benchDotPath},
	}
	for _, arm := range arms {
		b.Run(arm.name, benchCheckPathRow(arm.path))
	}
}

// benchScanRow prices one scan on one path, without the typed refusal on top,
// so the allocation column is the scan's own and not errs.Wrap's.
func benchScanRow(scan func(string) bool, path string) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			benchBool = scan(path)
		}
	}
}

// BenchmarkPathScan prices the two scans separately, which is what attributes
// the checks' cost between them.
func BenchmarkPathScan(b *testing.B) {
	arms := []struct {
		// name describes the path shape.
		name string
		// path is the escaped path scanned.
		path string
	}{
		{name: "Plain", path: benchPlainPath},
		{name: "UUID", path: benchUUIDPath},
	}
	for _, arm := range arms {
		b.Run(arm.name+"/Separator", benchScanRow(hasEncodedSeparator, arm.path))
		b.Run(arm.name+"/DotSegment", benchScanRow(hasDotSegment, arm.path))
	}
}

// benchPatterns builds n anchored allow patterns, each naming a distinct
// endpoint, in the shape a real allowlist takes.
func benchPatterns(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "/v1/resource"+strconv.Itoa(i)+"/[^/]+")
	}
	return out
}

// benchPolicyRow prices one policy against one request value.
func benchPolicyRow(policy corenet.Policy, req corenet.RequestValue) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			benchErr = policy.Allow(req)
		}
	}
}

// BenchmarkAllowPathsScaling is the number a consumer needs and does not have.
//
// pkg/v1/client/BENCH.md recommends denying by default and enumerating what is
// allowed. That recommendation is only affordable if the enumeration is cheap,
// and the enumeration is a LINEAR scan of compiled patterns. This measures the
// slope at 1, 5, 10, 25 and 50 patterns, matching first, matching last, and not
// matching at all — the last being the shape of every refused request.
func BenchmarkAllowPathsScaling(b *testing.B) {
	for _, n := range []int{1, 5, 10, 25, 50} {
		policy, err := AllowPaths(benchPatterns(n)...)
		if err != nil {
			b.Fatalf("compiling %d patterns: %v", n, err)
		}
		arms := []struct {
			// name describes where in the list the answer is found.
			name string
			// path is the request path judged.
			path string
		}{
			{name: "First", path: "/v1/resource0/abc"},
			{name: "Last", path: "/v1/resource" + strconv.Itoa(n-1) + "/abc"},
			{name: "NoMatch", path: "/v1/absent/abc"},
		}
		for _, arm := range arms {
			b.Run(strconv.Itoa(n)+"/"+arm.name, benchPolicyRow(policy, benchValue(arm.path)))
		}
	}
}

// BenchmarkPoliciesShape prices the composition a consumer is told to write —
// AllowMethods + DenyPaths + AllowPaths — which runs checkPath once per PATH
// policy, so the path scan is paid TWICE per request.
func BenchmarkPoliciesShape(b *testing.B) {
	allow, aerr := AllowPaths(benchPatterns(10)...)
	if aerr != nil {
		b.Fatalf("compiling the allow list: %v", aerr)
	}
	deny, derr := DenyPaths("/v1/resource0/secret")
	if derr != nil {
		b.Fatalf("compiling the deny list: %v", derr)
	}
	policy := Policies(AllowMethods(http.MethodGet), deny, allow)
	arms := []struct {
		// name describes the path shape.
		name string
		// path is the request path judged.
		path string
	}{
		{name: "Plain", path: "/v1/resource0/abc"},
		{name: "UUID", path: "/v1/resource0/9F8E7D6C-1234-4ABC-9DEF-0123456789AB"},
	}
	for _, arm := range arms {
		b.Run(arm.name, benchPolicyRow(policy, benchValue(arm.path)))
	}
}

// benchClient assembles a Client over a stub transport, which is the only way
// to price Do without a socket: New builds its own *http.Transport.
func benchClient(body io.ReadCloser, contentLength int64) *Client {
	transport := newBenchTransport(body)
	transport.resp.ContentLength = contentLength
	guarded := &guard{
		next:     transport,
		policy:   benchAllowAll,
		maxBytes: defaultMaxResponseSize,
	}
	return &Client{http: &http.Client{Transport: guarded}}
}

// benchDoRow prices one full Do, asserting the payload arrived whole so a
// truncating regression cannot pass as a speed-up.
func benchDoRow(c *Client, req *http.Request, want int) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(want))
		b.ResetTimer()
		for range b.N {
			resp, err := c.Do(req)
			if err != nil {
				b.Fatalf("do: %v", err)
			}
			if len(resp.Body) != want {
				b.Fatalf("read %d bytes, want %d", len(resp.Body), want)
			}
		}
	}
}

// BenchmarkDoBodySize prices the response read across three payload sizes and
// the two things a peer can say about the length.
//
// Announced is a peer sending an honest Content-Length, which is what lets the
// buffer be sized once. Unknown is a chunked or HTTP/2 body, which net/http
// reports as -1 — there the read falls back to io.ReadAll, which starts small
// and grows by append, so it reallocates about a dozen times and allocates
// roughly twice the payload.
//
// The 1 MiB pair is deliberately identical: that size is past maxPresizedRead,
// so the announced case takes the same io.ReadAll branch as the unknown one.
// The 64 KiB pair, where the branch differs, is what proves the two arms are
// not accidentally running the same code.
func BenchmarkDoBodySize(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10, 1 << 20} {
		arms := []struct {
			// name describes what the peer announced.
			name string
			// contentLength is what net/http reports for such a response.
			contentLength int64
		}{
			{name: "Announced", contentLength: int64(size)},
			{name: "Unknown", contentLength: -1},
		}
		for _, arm := range arms {
			c := benchClient(&benchReader{payload: make([]byte, size)}, arm.contentLength)
			b.Run(strconv.Itoa(size)+"/"+arm.name,
				benchDoRow(c, benchRequest(b, benchPlainPath), size))
		}
	}
}

// BenchmarkDoLyingPeer prices the case the pre-sizing bound exists for: a peer
// that answers a ten-byte body with a Content-Length it has no intention of
// honouring.
//
// The column that matters is B/op, and the two arms are the whole argument.
// AtTheCeiling claims the configured MaxResponseSize, which is past the bound,
// so no reservation is made at all. AtTheBound claims exactly maxPresizedRead,
// which is the most a lying peer can ever extract — and it must stay there
// however much larger the ceiling is configured to be.
func BenchmarkDoLyingPeer(b *testing.B) {
	arms := []struct {
		// name describes the size of the lie.
		name string
		// claimed is the Content-Length the peer announces for ten bytes.
		claimed int64
	}{
		{name: "AtTheCeiling", claimed: defaultMaxResponseSize},
		{name: "AtTheBound", claimed: maxPresizedRead},
	}
	for _, arm := range arms {
		c := benchClient(&benchReader{payload: make([]byte, benchTinyBody)}, arm.claimed)
		b.Run(arm.name, benchDoRow(c, benchRequest(b, benchPlainPath), benchTinyBody))
	}
}

// benchLowercaseHeaders is the configuration a caller writes when nothing tells
// them the spelling matters.
var benchLowercaseHeaders = map[string]string{
	"x-request-source": "sdk",
	"accept":           "application/json",
}

// benchHeadersRow prices applying one configured default set to a fresh header
// map, which is what every request does.
func benchHeadersRow(c *Client, req *http.Request) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			req.Header = http.Header{}
			c.applyHeaders(req)
		}
	}
}

// BenchmarkApplyHeaders prices the configured default headers, with keys spelled
// canonically and not.
//
// http.Header.Get and http.Header.Set both run CanonicalMIMEHeaderKey, which
// allocates for a name that is neither already canonical nor one of net/http's
// interned common names — so "x-request-source" costs two allocations per
// request and "accept" costs none, for the same misspelling.
//
// The third arm is the fix: the same lowercase configuration, canonicalised
// once at construction as New now does. The middle arm is kept as the permanent
// record of what that removes.
func BenchmarkApplyHeaders(b *testing.B) {
	arms := []struct {
		// name describes the spelling of the configured keys.
		name string
		// headers is the configured default set.
		headers map[string]string
	}{
		{name: "Canonical", headers: map[string]string{
			"X-Request-Source": "sdk",
			"Accept":           "application/json",
		}},
		{name: "NonCanonical", headers: benchLowercaseHeaders},
		{name: "NonCanonicalFixedAtConstruction", headers: canonicalHeaders(benchLowercaseHeaders)},
	}
	for _, arm := range arms {
		b.Run(arm.name, benchHeadersRow(&Client{headers: arm.headers}, benchRequest(b, benchPlainPath)))
	}
}

// BenchmarkResolve prices the path-to-URL resolution Get performs, which is the
// one place a query is encoded.
func BenchmarkResolve(b *testing.B) {
	base, err := url.Parse(benchOrigin)
	if err != nil {
		b.Fatalf("parsing the base: %v", err)
	}
	c := &Client{base: base}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, rerr := c.resolve(benchPlainPath, nil); rerr != nil {
			b.Fatalf("resolve: %v", rerr)
		}
	}
}

// benchEscapedPathRow prices url.URL.EscapedPath on one parsed URL.
func benchEscapedPathRow(parsed *url.URL) func(*testing.B) {
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			benchString = parsed.EscapedPath()
		}
	}
}

// BenchmarkEscapedPath prices url.URL.EscapedPath on the two shapes that matter,
// because guard.RoundTrip used to call it twice per request. It is not a field
// read: with RawPath set it re-validates and unescapes.
func BenchmarkEscapedPath(b *testing.B) {
	arms := []struct {
		// name describes the path shape.
		name string
		// path is the path appended to the origin before parsing.
		path string
	}{
		{name: "Plain", path: benchPlainPath},
		{name: "Encoded", path: benchEncodedPath},
	}
	for _, arm := range arms {
		parsed, err := url.Parse(benchOrigin + arm.path)
		if err != nil {
			b.Fatalf("parsing %q: %v", arm.path, err)
		}
		b.Run(arm.name, benchEscapedPathRow(parsed))
	}
}

// BenchmarkDotSegmentDepth prices the per-segment scan against segment COUNT,
// which is what turns a per-segment allocation into a per-request bill.
func BenchmarkDotSegmentDepth(b *testing.B) {
	benchScanRow(hasDotSegment, benchDeepPath)(b)
}
