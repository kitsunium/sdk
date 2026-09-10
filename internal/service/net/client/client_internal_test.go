// Package client — the URL resolution and transport assembly.
package client

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_parseBase pins that a base URL is validated at CONSTRUCTION.
//
// A base naming no origin would silently yield relative requests the transport
// cannot send, and the failure would surface on the first call rather than at
// startup — where an operator is actually watching.
func Test_parseBase(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     string
		wantNil bool
		wantErr bool
	}
	tests := []tc{
		{name: "an https origin", raw: "https://sdm.core.svc"},
		{name: "an origin with a port", raw: "https://sdm.core.svc:8443"},
		{name: "an origin with a path prefix", raw: "https://sdm.core.svc/api"},
		{name: "an http origin", raw: "http://127.0.0.1:8080"},
		//: an empty base means the caller passes absolute URLs, which is a
		//: supported mode rather than a mistake.
		{name: "an empty base", raw: "", wantNil: true},
		{name: "no scheme", raw: "sdm.core.svc", wantErr: true},
		{name: "no host", raw: "https://", wantErr: true},
		{name: "a bare path", raw: "/api", wantErr: true},
		{name: "an unparseable URL", raw: "://nope", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := parseBase(c.raw)

		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeInvalidAddress) {
				t.Fatalf("parseBase(%q) = %v, want INVALID_ADDRESS", c.raw, err)
			}
			//: a refused base must hand back nothing, or a caller checking only
			//: the value would build a client around a broken origin.
			if got != nil {
				t.Errorf("parseBase(%q) returned a base beside the error", c.raw)
			}
			return
		}
		if err != nil {
			t.Fatalf("parseBase(%q) = %v, want nil", c.raw, err)
		}
		if (got == nil) != c.wantNil {
			t.Fatalf("parseBase(%q) returned nil = %v, want %v", c.raw, got == nil, c.wantNil)
		}
		if got != nil && (got.Scheme == "" || got.Host == "") {
			t.Errorf("parseBase(%q) accepted a base with no origin: %+v", c.raw, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Client_resolve pins the query encoding, the base resolution, and the
// refusal of a reference that carries its own ORIGIN.
//
// Encoding the query HERE is what stops each call site inventing its own — and
// a call site that concatenated a raw value would produce a path the policy
// judges differently from the one that goes on the wire.
//
// The origin check is the one that matters most. RFC 3986 reads "//other/path"
// as an authority rather than as a path, so ResolveReference REPLACES the base's
// host with it — and the built-in policies judge only the method and the path,
// so nothing downstream sees the substitution. A caller passing a
// caller-supplied identifier straight into Get would reach whatever peer that
// identifier named.
func Test_Client_resolve(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// base is the client's configured base URL; empty means none.
		base string
		// path is what the caller passes to Get.
		path string
		// query is encoded onto it.
		query url.Values
		// want is the resolved target.
		want string
		// wantCode is the refusal, or zero when the path resolves.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a path against a base", base: "https://h", path: "/v1/x", want: "https://h/v1/x"},
		{
			name:  "a query is encoded",
			base:  "https://h",
			path:  "/v1/x",
			query: url.Values{"a": []string{"b c"}},
			want:  "https://h/v1/x?a=b+c",
		},
		{
			//: a value carrying a delimiter is escaped rather than reaching the
			//: wire as another parameter.
			name:  "a query value with a delimiter is escaped",
			base:  "https://h",
			path:  "/v1/x",
			query: url.Values{"a": []string{"b&c=d"}},
			want:  "https://h/v1/x?a=b%26c%3Dd",
		},
		{
			//: ResolveReference applies RFC 3986 dot-segment removal, so the
			//: LITERAL form collapses before any policy sees it — which is why
			//: the policy still checks the encoded form itself.
			name: "a literal dot segment collapses",
			base: "https://h", path: "/v1/../x", want: "https://h/x",
		},
		{
			name: "an encoded dot segment survives for the policy to catch",
			base: "https://h", path: "/v1/%2e%2e", want: "https://h/v1/%2e%2e",
		},
		{
			//: with no base the caller supplies absolute URLs.
			name: "an absolute URL with no base",
			base: "", path: "https://other/v1/x", want: "https://other/v1/x",
		},
		{name: "an unparseable path", base: "https://h", path: "://nope", wantCode: corenet.CodeInvalidAddress},
		{
			//: "//host/path" is an AUTHORITY, so resolving it against the base
			//: replaces the host — and the policy, which judges only method and
			//: path, never sees that it happened.
			name: "a scheme-relative reference against a base",
			base: "https://h", path: "//evil.example/v1/x", wantCode: corenet.CodeUnsafePath,
		},
		{
			name: "a scheme-relative reference naming a port",
			base: "https://h", path: "//127.0.0.1:8080/v1/x", wantCode: corenet.CodeUnsafePath,
		},
		{
			//: an absolute URL is the same substitution spelled out in full.
			name: "an absolute URL against a base",
			base: "https://h", path: "https://evil.example/v1/x", wantCode: corenet.CodeUnsafePath,
		},
		{
			name: "a scheme with no authority against a base",
			base: "https://h", path: "mailto:someone@example", wantCode: corenet.CodeUnsafePath,
		},
		{
			//: a relative path is still resolved against the base, because that
			//: is what a relative reference means.
			name: "a relative path against a base",
			base: "https://h", path: "v1/x", want: "https://h/v1/x",
		},
		{
			//: with no base there is nothing to substitute, so the caller's own
			//: absolute URL is the request.
			name: "a scheme-relative reference with no base",
			base: "", path: "//other/v1/x", want: "//other/v1/x",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		base, berr := parseBase(c.base)
		if berr != nil {
			t.Fatalf("parseBase(%q) = %v", c.base, berr)
		}
		client := &Client{base: base}

		got, err := client.resolve(c.path, c.query)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("resolve(%q) = %v, want code %v", c.path, err, c.wantCode)
			}
			//: a refused reference resolves to nothing, so no caller checking
			//: only the value can send a request to a peer it never named.
			if got != "" {
				t.Errorf("resolve(%q) returned %q beside the error", c.path, got)
			}
			//: the refusal never echoes the path, which may carry a secret the
			//: caller pasted into it.
			if strings.Contains(err.Error(), c.path) {
				t.Errorf("the refusal echoes the path: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("resolve(%q) = %v, want nil", c.path, err)
		}
		if got != c.want {
			t.Errorf("resolve(%q) = %q, want %q", c.path, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_newTransport pins that every PHASE is bounded separately.
//
// One overall timeout cannot tell a slow peer from a large body: both look like
// "it took too long". Bounding the dial, the handshake and the response headers
// individually is what makes the difference diagnosable — and what stops a peer
// that accepts the connection and then says nothing from consuming the whole
// budget before a single byte arrives.
func Test_newTransport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  corenet.ClientConfig
	}
	tests := []tc{
		{name: "the defaults", cfg: withDefaults(corenet.ClientConfig{})},
		{
			name: "explicit budgets",
			cfg: withDefaults(corenet.ClientConfig{
				DialTimeout:      corenet.DurationValue(time.Second),
				HandshakeTimeout: corenet.DurationValue(2 * time.Second),
				ResponseTimeout:  corenet.DurationValue(3 * time.Second),
			}),
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		tr := newTransport(c.cfg, corenet.IdentityValue{})

		if tr.DialContext == nil {
			t.Error("the transport has no bounded dialer")
		}
		//: an unbounded handshake lets a peer hold a connection open through a
		//: TLS negotiation it never intends to finish.
		if tr.TLSHandshakeTimeout != c.cfg.HandshakeTimeout.Duration() {
			t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, c.cfg.HandshakeTimeout.Duration())
		}
		//: an unbounded header wait is the same trick one layer up.
		if tr.ResponseHeaderTimeout != c.cfg.ResponseTimeout.Duration() {
			t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, c.cfg.ResponseTimeout.Duration())
		}
		//: the identity's config reaches the transport, or mutual TLS would
		//: silently degrade to server authentication only.
		if tr.TLSClientConfig == nil {
			t.Error("the transport carries no TLS configuration")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_redirectLimiter pins the chain cap. Every hop re-enters the transport and
// is authorised individually, so a 302 cannot walk the client out of its allowed
// surface — but an unbounded chain is still a way to make one call consume an
// unbounded amount of time, so the count is capped too.
func Test_redirectLimiter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		limit   int
		hops    int
		wantErr bool
	}
	tests := []tc{
		{name: "the first hop within a budget of three", limit: 3, hops: 0},
		{name: "the third hop within a budget of three", limit: 3, hops: 2},
		{name: "one hop past the budget", limit: 3, hops: 3, wantErr: true},
		{name: "well past the budget", limit: 3, hops: 10, wantErr: true},
		//: a budget of zero refuses the very first redirect.
		{name: "a budget of zero", limit: 0, hops: 0, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		limiter := redirectLimiter(c.limit)
		via := make([]*http.Request, c.hops)

		err := limiter(nil, via)

		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeTooManyRedirects) {
				t.Fatalf("the limiter = %v, want TOO_MANY_REDIRECTS", err)
			}
			//: the limit is named so an operator can raise it deliberately.
			var named bool
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "limit" {
					named = true
				}
			}
			if !named {
				t.Errorf("the refusal does not name the limit: %v", errs.FieldsOf(err))
			}
			return
		}
		if err != nil {
			t.Fatalf("the limiter = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_unwrapClientError pins the envelope removal.
//
// http.Client wraps EVERY failure in a *url.Error, including our own typed
// refusals — so without this a caller's errors.Is against a domain sentinel
// would still match through Unwrap, but errs.CodeOf would read the envelope and
// the *url.Error's own text quotes the full URL into whatever logs it.
func Test_unwrapClientError(t *testing.T) {
	t.Parallel()
	denied := errs.Wrap(corenet.RequestDenied, errs.WrapParams{}, errs.String("why", "no"))

	type tc struct {
		name string
		err  error
		//: the code the result must carry, or zero when there is none.
		wantCode errs.Code
	}
	tests := []tc{
		{
			name:     "a wrapped policy refusal",
			err:      &url.Error{Op: "Get", URL: "https://h/v1/x", Err: denied},
			wantCode: corenet.CodeRequestDenied,
		},
		{
			name:     "an unwrapped refusal passes through",
			err:      denied,
			wantCode: corenet.CodeRequestDenied,
		},
		{
			name: "a url.Error with no cause",
			err:  &url.Error{Op: "Get", URL: "https://h/v1/x"},
		},
		{name: "a plain error", err: errors.New("boom")},
		{name: "no error at all"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := unwrapClientError(c.err)

		if c.wantCode == 0 {
			//: nothing to unwrap: the value comes back as it went in.
			if c.err == nil && got != nil {
				t.Errorf("unwrapClientError(nil) = %v, want nil", got)
			}
			return
		}
		if !errs.HasCode(got, c.wantCode) {
			t.Fatalf("unwrapClientError = %v, want code %v", got, c.wantCode)
		}
		//: the envelope is gone, so nothing downstream logs the full URL.
		var wrapped *url.Error
		if errors.As(got, &wrapped) {
			t.Errorf("the *url.Error envelope survived: %v", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_bodyError pins the split. A refusal that already carries a domain code —
// the size ceiling's own — is handed back UNTOUCHED so errors.Is and
// errs.HasCode keep matching it. Anything else came from the transport and would
// otherwise leave this package as a bare stdlib error, which the error model
// does not permit; its text is deliberately not echoed, because a transport
// message can name the internal address.
func Test_bodyError(t *testing.T) {
	t.Parallel()
	typed := errs.Wrap(corenet.ResponseTooLarge, errs.WrapParams{}, errs.Int64("limit", 8))

	type tc struct {
		name     string
		err      error
		wantCode errs.Code
		//: whether the result must be the very same error value.
		wantSame bool
	}
	tests := []tc{
		{name: "the ceiling's own refusal", err: typed, wantCode: corenet.CodeResponseTooLarge, wantSame: true},
		{name: "a transport failure", err: errors.New("connection reset by peer"), wantCode: corenet.CodeCallFailed},
		{name: "a deadline", err: errors.New("context deadline exceeded"), wantCode: corenet.CodeCallFailed},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := bodyError(c.err)

		if !errs.HasCode(got, c.wantCode) {
			t.Fatalf("bodyError = %v, want code %v", got, c.wantCode)
		}
		if c.wantSame {
			if !errors.Is(got, c.err) {
				t.Errorf("bodyError rewrapped a typed refusal: %v", got)
			}
			return
		}
		//: the transport's own text must not be echoed — it can name the
		//: internal address.
		if errors.Is(got, c.err) {
			t.Errorf("bodyError carried the transport error through: %v", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_Client_applyHeaders pins that a CALLER-set header always wins. Configured
// defaults exist to spare every call site from repeating them, not to override a
// deliberate choice — a request that set its own Accept means it.
func Test_Client_applyHeaders(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		defaults map[string]string
		preset   map[string]string
		want     map[string]string
	}
	tests := []tc{
		{name: "no defaults and no presets", want: map[string]string{}},
		{
			name:     "a default fills an unset header",
			defaults: map[string]string{"User-Agent": "sdk/1"},
			want:     map[string]string{"User-Agent": "sdk/1"},
		},
		{
			//: the caller's own value survives.
			name:     "a preset beats the default",
			defaults: map[string]string{"User-Agent": "sdk/1"},
			preset:   map[string]string{"User-Agent": "mine/2"},
			want:     map[string]string{"User-Agent": "mine/2"},
		},
		{
			name:     "several defaults, one overridden",
			defaults: map[string]string{"User-Agent": "sdk/1", "Accept": "application/json"},
			preset:   map[string]string{"Accept": "text/plain"},
			want:     map[string]string{"User-Agent": "sdk/1", "Accept": "text/plain"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://h/v1/x", nil)
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		for k, v := range c.preset {
			req.Header.Set(k, v)
		}
		client := &Client{headers: c.defaults}

		client.applyHeaders(req)

		for k, want := range c.want {
			if got := req.Header.Get(k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// hintedReader hands back a fixed payload while ANNOUNCING whatever length the
// test wants, which is the only way to model a peer whose Content-Length and
// body disagree.
type hintedReader struct {
	// payload is what the peer actually sends.
	payload []byte
	// off is the read cursor.
	off int
}

// Read implements io.Reader over the remaining payload.
func (r *hintedReader) Read(p []byte) (n int, err error) {
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

// Test_readBody pins that the peer's Content-Length is a HINT and never a
// promise, in both directions.
//
// The pre-sizing exists because io.ReadAll starts at bytes.MinRead and grows by
// append, so it allocates roughly twice a large body and copies it a dozen
// times. Taking the hint at face value would trade that for something worse: a
// peer answering ten bytes with a Content-Length of defaultMaxResponseSize
// would make this client reserve the whole ceiling per request for ten bytes of
// work, which is a memory-amplification introduced by a performance fix.
//
// So the hint is bounded by maxPresizedRead and ignored above it, and the
// reserved capacity is asserted here rather than left to the benchmark — a
// benchmark is not run in CI and this is the bound that keeps a lying peer
// cheap.
//
// MUTATION: removing the `hint > maxPresizedRead` clause so the claim is
// believed all the way to the ceiling fails the "lying at the ceiling" case
// with `got ... bytes of capacity, want at most ...` — the reservation reported
// there is defaultMaxResponseSize plus bytes.MinRead, bought with a ten-byte
// reply, which is exactly the amplification the bound refuses.
func Test_readBody(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the peer's behaviour.
		name string
		// sent is how many bytes the peer actually sends.
		sent int
		// hint is the Content-Length it announces; negative means unknown.
		hint int64
		// wantMaxCap bounds the capacity the read may reserve.
		wantMaxCap int
	}
	tests := []tc{
		{name: "an honest small body", sent: 1024, hint: 1024, wantMaxCap: 1024 + bytes.MinRead},
		{name: "an honest body at the bound", sent: 4096, hint: 4096, wantMaxCap: 4096 + bytes.MinRead},
		{
			//: -1 is what net/http reports for a chunked or HTTP/2 body.
			name: "an unknown length", sent: 4096, hint: -1, wantMaxCap: 3 * 4096,
		},
		{name: "no length at all", sent: 512, hint: 0, wantMaxCap: 3 * 512},
		{
			//: past the bound nothing is reserved on the peer's word at all.
			name: "a body larger than the bound", sent: 3 * int(maxPresizedRead), hint: 3 * maxPresizedRead,
			wantMaxCap: 3 * 3 * int(maxPresizedRead),
		},
		{
			//: the lie the bound exists for.
			name: "a peer lying at the ceiling", sent: 10, hint: defaultMaxResponseSize,
			wantMaxCap: int(maxPresizedRead) + bytes.MinRead,
		},
		{
			//: the most a lying peer can ever extract.
			name: "a peer lying at the bound", sent: 10, hint: maxPresizedRead,
			wantMaxCap: int(maxPresizedRead) + bytes.MinRead,
		},
		{
			//: a peer that undersells itself must not be truncated.
			name: "a peer that sends more than it claimed", sent: 8192, hint: 16,
			wantMaxCap: 3 * 8192,
		},
		{name: "an empty body with a length", sent: 0, hint: 0, wantMaxCap: bytes.MinRead},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		payload := make([]byte, c.sent)
		for i := range payload {
			payload[i] = byte(i)
		}

		got, err := readBody(&hintedReader{payload: payload}, c.hint)
		if err != nil {
			t.Fatalf("readBody = %v, want nil", err)
		}
		//: every byte the peer sent must arrive, whatever it announced.
		if !bytes.Equal(got, payload) {
			t.Fatalf("read %d bytes, want the %d the peer sent", len(got), c.sent)
		}
		//: and the reservation must never scale with the peer's claim.
		if cap(got) > c.wantMaxCap {
			t.Errorf("a peer claiming %d bytes for %d got %d bytes of capacity, want at most %d",
				c.hint, c.sent, cap(got), c.wantMaxCap)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_canonicalHeaders pins that the configured default names are normalised
// ONCE, at construction.
//
// http.Header.Get and http.Header.Set both canonicalise their argument, and
// that conversion allocates for a name that is neither already canonical nor
// one of net/http's interned common names. A caller who writes
// "x-request-source" — and nothing tells them not to — therefore paid two
// allocations per header per request for a configuration that was never wrong.
//
// MUTATION: returning `configured` unchanged fails every mis-spelled case at
// once — `"User-Agent" = "", want "sdk/1"` beside
// `key "USER-AGENT" survived canonicalisation`, and the same pair for "accept"
// and "x-request-source". The map handed to applyHeaders would still carry the
// caller's spelling, so every request would re-derive the canonical form.
func Test_canonicalHeaders(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the configured spelling.
		name string
		// configured is what the caller wrote.
		configured map[string]string
		// want is the map applyHeaders must be given.
		want map[string]string
	}
	tests := []tc{
		{name: "no defaults at all", configured: nil, want: nil},
		{name: "an empty map", configured: map[string]string{}, want: nil},
		{
			name:       "an already canonical name is untouched",
			configured: map[string]string{"X-Request-Source": "sdk"},
			want:       map[string]string{"X-Request-Source": "sdk"},
		},
		{
			name:       "a lowercase custom name is canonicalised",
			configured: map[string]string{"x-request-source": "sdk"},
			want:       map[string]string{"X-Request-Source": "sdk"},
		},
		{
			name:       "a lowercase common name is canonicalised too",
			configured: map[string]string{"accept": "application/json"},
			want:       map[string]string{"Accept": "application/json"},
		},
		{
			name:       "a shouted name is canonicalised",
			configured: map[string]string{"USER-AGENT": "sdk/1"},
			want:       map[string]string{"User-Agent": "sdk/1"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()

		got := canonicalHeaders(c.configured)

		if len(got) != len(c.want) {
			t.Fatalf("canonicalHeaders produced %d entries, want %d", len(got), len(c.want))
		}
		for name, value := range c.want {
			//: the canonical spelling must be the KEY, not merely reachable.
			if got[name] != value {
				t.Errorf("%q = %q, want %q", name, got[name], value)
			}
		}
		for name := range c.configured {
			//: a name the caller mis-spelled must not survive as a second key.
			if _, ok := got[name]; ok && http.CanonicalHeaderKey(name) != name {
				t.Errorf("key %q survived canonicalisation", name)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
