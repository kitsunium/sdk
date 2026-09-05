// Package client_test — the guarded HTTP client as a caller sees it.
package client_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcclient "github.com/kitsunium/sdk/internal/service/net/client"
)

// newServer starts a test upstream and records whether it was ever reached.
func newServer(t *testing.T, handler http.HandlerFunc) (srv *httptest.Server, reached *bool) {
	t.Helper()
	hit := false
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hit
}

// newClient builds a guarded client against srv carrying the read-only policy.
func newClient(t *testing.T, srv *httptest.Server, cfg corenet.ClientConfig) *svcclient.Client {
	t.Helper()
	cfg.BaseURL = srv.URL
	c, err := svcclient.New(cfg, corenet.IdentityValue{}, buildReadOnly(t), nil)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// newObservedClient builds the same client with a hook collecting every record.
func newObservedClient(t *testing.T, srv *httptest.Server, cfg corenet.ClientConfig) (c *svcclient.Client, calls *[]corenet.CallValue) {
	t.Helper()
	cfg.BaseURL = srv.URL
	observed := make([]corenet.CallValue, 0, 4)
	built, err := svcclient.New(cfg, corenet.IdentityValue{}, buildReadOnly(t),
		func(call corenet.CallValue) { observed = append(observed, call) })
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return built, &observed
}

// writeOrFail writes a test response body, failing the test rather than
// discarding the error — a dropped write would make an assertion pass on a
// body that was never actually sent.
func writeOrFail(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

// closeQuietly closes c and reports a failure rather than discarding it.
func closeQuietly(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestNew pins that a client cannot be built into an unsafe shape.
//
// A nil policy is refused rather than defaulted to "allow everything": an
// unguarded egress path wearing the name of a guarded one is worse than none,
// because every reader downstream will trust the name. A malformed base is
// refused at construction for the same reason a pattern is — the alternative is
// a failure on the first call, in production, far from whoever wrote the config.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// base is the configured base URL.
		base string
		// noPolicy omits the policy entirely.
		noPolicy bool
		// wantCode is the code a refusal must carry, or zero when it succeeds.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "an origin and a policy", base: "https://sdm:8443"},
		{name: "a base with a path prefix", base: "https://sdm:8443/api"},
		//: no base means the caller passes absolute URLs, which is supported.
		{name: "no base at all", base: ""},
		{name: "no policy", base: "https://sdm:8443", noPolicy: true, wantCode: corenet.CodeRequestDenied},
		{name: "a base with no scheme", base: "sdm:8443", wantCode: corenet.CodeInvalidAddress},
		{name: "a base with no host", base: "https://", wantCode: corenet.CodeInvalidAddress},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var policy corenet.Policy
		if !c.noPolicy {
			policy = buildReadOnly(t)
		}

		got, err := svcclient.New(corenet.ClientConfig{BaseURL: c.base}, corenet.IdentityValue{}, policy, nil)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("New = %v, want code %v", err, c.wantCode)
			}
			//: a refused construction hands back nothing, or a caller checking
			//: only the value would hold an unguarded client.
			if got != nil {
				t.Error("New returned a client beside the error")
			}
			return
		}
		if err != nil {
			t.Fatalf("New = %v, want nil", err)
		}
		if got == nil || got.HTTP() == nil {
			t.Fatal("New returned no usable client")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClient_HTTP is the security guarantee of this package.
//
// The refused request must not reach the upstream at all — and crucially, it
// must not reach it even when the caller bypasses the typed Client entirely and
// forges its own request through the raw *http.Client. That escape hatch is the
// realistic attack on this design: a third-party SDK handed the client, or a
// contributor who "just needs one more call". If the check lived in Do rather
// than in the RoundTripper, this test would fail.
func TestClient_HTTP(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// method and path are what the forged request carries.
		method string
		path   string
		// wantCode is the refusal, or zero when the request is admitted.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a listed read is admitted", method: http.MethodGet, path: "/v1/subscribers"},
		{name: "a write verb", method: http.MethodDelete, path: "/v1/subscribers", wantCode: corenet.CodeRequestDenied},
		{name: "a write body", method: http.MethodPost, path: "/v1/subscribers", wantCode: corenet.CodeRequestDenied},
		{name: "an unlisted path", method: http.MethodGet, path: "/v1/context-data", wantCode: corenet.CodeRequestDenied},
		{name: "a denied path", method: http.MethodGet, path: "/v1/authentication/all", wantCode: corenet.CodeRequestDenied},
		{name: "an invalid dot segment", method: http.MethodGet, path: "/v1/supi/..", wantCode: corenet.CodeUnsafePath},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, reached := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		client := newClient(t, srv, corenet.ClientConfig{})

		//: forge a request through the raw client, deliberately bypassing Do.
		req, rerr := http.NewRequestWithContext(t.Context(), c.method, srv.URL+c.path, nil)
		if rerr != nil {
			t.Fatalf("build request: %v", rerr)
		}
		resp, err := client.HTTP().Do(req)

		if c.wantCode == 0 {
			if err != nil {
				t.Fatalf("an admitted request failed: %v", err)
			}
			closeQuietly(t, resp.Body)
			if !*reached {
				t.Error("an admitted request never reached the upstream")
			}
			return
		}
		//: an admitted write would mean the guarantee is a convention, not a fact.
		if err == nil {
			closeQuietly(t, resp.Body)
			t.Fatal("the raw client performed a request the policy forbids")
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("HTTP().Do = %v, want code %v", err, c.wantCode)
		}
		//: a refusal must leave no trace on the network, not even a connection.
		if *reached {
			t.Error("the upstream was contacted despite the refusal")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClient_Get_ReportsTheStatus pins that the status is carried through rather
// than collapsed into "worked" and "did not".
//
// A 204 with an empty body is a normal outcome — the SDM returns it for a
// subscriber with no session, so a client that treats "no body" as a failure
// breaks the nominal case. A 4xx yields BOTH the response and an error: whoever
// is diagnosing the failure needs the body, and whoever never thought about the
// status still gets an error.
func TestClient_Get_ReportsTheStatus(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// status is what the upstream answers.
		status int
		// body is the payload it sends.
		body string
		// wantErr is whether the caller must be told the call failed.
		wantErr bool
	}
	tests := []tc{
		{name: "a 200 with a body", status: http.StatusOK, body: `{"supi":"1"}`},
		{name: "a 204 with no body", status: http.StatusNoContent},
		{name: "an invalid request", status: http.StatusBadRequest, body: `{"detail":"exclusive"}`, wantErr: true},
		{name: "an upstream failure", status: http.StatusBadGateway, body: "upstream is down", wantErr: true},
		{name: "a refusal from the upstream", status: http.StatusForbidden, body: "nope", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			//: a 204 may not carry a body at all.
			if c.body != "" {
				writeOrFail(t, w, c.body)
			}
		})
		client := newClient(t, srv, corenet.ClientConfig{})

		resp, err := client.Get(t.Context(), "/v1/subscribers", nil)

		//: the status is reported whether or not the call is considered a failure.
		if resp.Status != c.status {
			t.Fatalf("Status = %d, want %d", resp.Status, c.status)
		}
		if !c.wantErr {
			if err != nil {
				t.Fatalf("a %d was reported as an error: %v", c.status, err)
			}
			return
		}
		if err == nil {
			t.Fatal("a non-success status was reported as success")
		}
		//: the body travels WITH the error; it is what names the actual problem.
		if !strings.Contains(string(resp.Body), c.body) {
			t.Errorf("Body = %q, want it to carry %q alongside the error", resp.Body, c.body)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClient_Get_CapsTheBody pins all three boundaries around the ceiling.
//
// MaxResponseSize is a maximum, so a body OF exactly that size is admissible
// and only a body past it is refused. Testing one side of the boundary is what
// let the off-by-one through: a wrapper that stops the moment it has handed
// back `limit` bytes cannot tell "the body ended exactly here" from "there is
// one more byte to come", and refuses both. And it FAILS rather than
// truncating — a silent truncation surfaces several layers away as an
// incomprehensible decode error on a body that looks complete.
func TestClient_Get_CapsTheBody(t *testing.T) {
	t.Parallel()
	const ceiling int64 = 128
	type tc struct {
		// name describes where the body sits relative to the ceiling.
		name string
		// size is the body length the upstream sends, in bytes.
		size int
		// wantErr is whether the ceiling must refuse that body.
		wantErr bool
	}
	tests := []tc{
		{name: "an empty body", size: 0},
		{name: "one byte under the ceiling", size: int(ceiling) - 1},
		{name: "exactly at the ceiling", size: int(ceiling)},
		{name: "one byte over the ceiling", size: int(ceiling) + 1, wantErr: true},
		{name: "far over the ceiling", size: 4096, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeOrFail(t, w, strings.Repeat("x", c.size))
		})
		client := newClient(t, srv, corenet.ClientConfig{MaxResponseSize: ceiling})

		resp, err := client.Get(t.Context(), "/v1/subscribers", nil)

		//: only a body PAST the ceiling may be refused.
		if c.wantErr {
			if !errs.HasCode(err, corenet.CodeResponseTooLarge) {
				t.Fatalf("a %d-byte body under a %d-byte ceiling: expected RESPONSE_TOO_LARGE, got %v",
					c.size, ceiling, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("a %d-byte body under a %d-byte ceiling was refused: %v", c.size, ceiling, err)
		}
		//: an admitted body must arrive whole; the ceiling never truncates.
		if len(resp.Body) != c.size {
			t.Fatalf("Body is %d bytes, want %d — the ceiling truncated instead of admitting",
				len(resp.Body), c.size)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClient_Get_EncodesTheQuery pins that a caller never escapes a query value
// by hand — one forgotten escape is all it takes, and the value that needs it is
// usually the one that arrived from outside.
func TestClient_Get_EncodesTheQuery(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// query is what the caller passes.
		query url.Values
		// want is the raw query the upstream must receive.
		want string
	}
	tests := []tc{
		{name: "no query at all"},
		{name: "a plain value", query: url.Values{"supi": {"208930000100001"}}, want: "supi=208930000100001"},
		{
			//: a space and a separator both have to be escaped, and a call site
			//: doing it by hand gets exactly this wrong.
			name:  "a value with a space and a separator",
			query: url.Values{"profileId": {"P tenant/a"}}, want: "profileId=P+tenant%2Fa",
		},
		{
			name:  "a value that would forge another parameter",
			query: url.Values{"a": {"b&admin=true"}}, want: "a=b%26admin%3Dtrue",
		},
		{
			//: url.Values sorts by key, so the wire form is deterministic.
			name:  "several parameters",
			query: url.Values{"b": {"2"}, "a": {"1"}}, want: "a=1&b=2",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(chan string, 1)
		srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			seen <- r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
		})
		client := newClient(t, srv, corenet.ClientConfig{})

		if _, err := client.Get(t.Context(), "/v1/subscribers", c.query); err != nil {
			t.Fatalf("Get = %v, want nil", err)
		}

		if got := <-seen; got != c.want {
			t.Errorf("the upstream received %q, want the escaped form %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClient_Get pins WHICH path the policy sees, and that
// no bare stdlib error escapes this package.
//
// Get resolves the caller's path against the base before anything judges it, and
// RFC 3986 resolution collapses LITERAL dot segments — so "/v1/supi/.." never
// reaches the policy as a traversal; it reaches it as "/v1/", which the
// allowlist simply does not cover. The percent-encoded spellings do NOT
// collapse, because resolution works on the escaped form, and those are exactly
// what the path guard exists for. Both are refused, and the distinction is worth
// pinning: the escape hatch does no resolution at all, so there "/v1/supi/.."
// arrives verbatim and it is the guard, not the allowlist, that stops it.
//
// The typing matters just as much. The ceiling's own refusal is already typed,
// which makes it easy to assume every error out of the body read is. It is not:
// the connection can die after the headers have been accepted, and that error
// arrives from net/http wearing no code at all — and its text names the
// internal address.
func TestClient_Get(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// path is what the caller asks for.
		path string
		// truncate makes the upstream promise more than it delivers.
		truncate bool
		// wantCode is the code the caller must receive, or zero when the request
		// is admitted.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a listed read", path: "/v1/subscribers"},
		{
			//: resolution against the base supplies the leading slash, so this is
			//: the same request as the one above rather than a rejected one.
			name: "a path relative to the base",
			path: "v1/subscribers",
		},
		{name: "a body cut short", path: "/v1/subscribers", truncate: true, wantCode: corenet.CodeCallFailed},
		{name: "a path the policy denies", path: "/v1/authentication/all", wantCode: corenet.CodeRequestDenied},
		{
			//: collapsed by resolution into "/v1/", which no pattern covers — so
			//: it is the allowlist that refuses it, not the path guard.
			name: "a literal dot segment", path: "/v1/supi/..", wantCode: corenet.CodeRequestDenied,
		},
		{
			//: resolution works on the escaped form, so this one survives intact
			//: and meets the guard.
			name: "an encoded dot segment", path: "/v1/supi/%2e%2e", wantCode: corenet.CodeUnsafePath,
		},
		{name: "an encoded separator", path: "/v1/supi/a%2fb", wantCode: corenet.CodeUnsafePath},
		{
			//: an authority, not a path: resolving it would replace the host.
			name: "a reference carrying its own origin", path: "//evil.example/v1/subscribers",
			wantCode: corenet.CodeUnsafePath,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			//: promise far more than is delivered, so the body dies mid-read.
			if c.truncate {
				w.Header().Set("Content-Length", "4096")
			}
			writeOrFail(t, w, "partial")
		})
		client := newClient(t, srv, corenet.ClientConfig{})

		_, err := client.Get(t.Context(), c.path, nil)

		if c.wantCode == 0 {
			if err != nil {
				t.Fatalf("Get(%q) = %v, want nil", c.path, err)
			}
			return
		}
		if err == nil {
			t.Fatal("a failing call was reported as success")
		}
		//: an untyped stdlib error escaping internal/service is the actual defect.
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("Get(%q) = an untyped %T: %v, want code %v", c.path, err, err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClient_Get_IsObserved pins the observation contract: exactly one record
// per outbound call, and that record must AGREE with what the caller was told.
//
// CallValue.Bytes was documented, re-exported as part of the public CallInfo,
// and assigned nowhere in the tree, because the hook fired as soon as the
// response headers arrived — strictly before the body exists to be measured. The
// same timing reported an over-sized body as a clean 200 with no error, which is
// the exact opposite of what happened. An audit trail that disagrees with the
// outcome is worse than none, because it is the one thing an operator trusts.
func TestClient_Get_IsObserved(t *testing.T) {
	t.Parallel()
	const payload string = "0123456789"
	type tc struct {
		// name describes the case.
		name string
		// path is what the caller asks for.
		path string
		// ceiling caps the response body; zero leaves the default.
		ceiling int64
		// wantStatus is the status the record must carry.
		wantStatus int
		// wantBytes is the byte count the record must carry.
		wantBytes int64
		// wantCode is the outcome recorded, or zero for a clean call.
		wantCode errs.Code
	}
	tests := []tc{
		{
			name:       "a successful call",
			path:       "/v1/subscribers",
			wantStatus: http.StatusOK, wantBytes: int64(len(payload)),
		},
		{
			//: an egress that never happened still belongs in the audit trail.
			name: "a call the policy refused",
			path: "/v1/authentication/all", wantCode: corenet.CodeRequestDenied,
		},
		{
			//: the refusal happens strictly after the headers, so a record emitted
			//: there reported a clean 200 for a call that failed. The byte count
			//: is what was actually taken off the wire — the ceiling plus the one
			//: probe byte that proved the body exceeded it.
			name: "a call that failed on its body",
			path: "/v1/subscribers", ceiling: 4,
			wantStatus: http.StatusOK, wantBytes: 5, wantCode: corenet.CodeResponseTooLarge,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeOrFail(t, w, payload)
		})
		client, observed := newObservedClient(t, srv, corenet.ClientConfig{MaxResponseSize: c.ceiling})

		_, err := client.Get(t.Context(), c.path, nil)

		if (err != nil) != (c.wantCode != 0) {
			t.Fatalf("Get = %v, want an error = %v", err, c.wantCode != 0)
		}
		//: exactly one record per call: a second would double every entry, which
		//: is how a call count stops meaning anything.
		if len(*observed) != 1 {
			t.Fatalf("the hook fired %d times, want 1", len(*observed))
		}
		record := (*observed)[0]
		if record.Status != c.wantStatus {
			t.Errorf("the record carries status %d, want %d", record.Status, c.wantStatus)
		}
		if record.Bytes != c.wantBytes {
			t.Errorf("the record carries %d bytes, want %d", record.Bytes, c.wantBytes)
		}
		if c.wantCode == 0 {
			//: a call that succeeded is not recorded as a failure.
			if record.Err != nil {
				t.Errorf("a successful call was recorded as %v", record.Err)
			}
			if record.Path != c.path {
				t.Errorf("the record carries path %q, want %q", record.Path, c.path)
			}
			if record.Duration <= 0 {
				t.Error("the record carries a non-positive duration")
			}
			return
		}
		//: the record must agree with what the caller was told.
		if !errs.HasCode(record.Err, c.wantCode) {
			t.Errorf("the call was recorded as %v, want code %v — the audit trail "+
				"disagrees with the outcome", record.Err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClient_Do_AppliesTheHeaders pins that configured headers spare every call
// site from repeating them, while a header the CALLER set always wins. A request
// that carries its own Accept means it; defaults exist to fill gaps, not to
// overrule a deliberate choice.
func TestClient_Do_AppliesTheHeaders(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// defaults are configured on the client.
		defaults map[string]string
		// preset is what the caller sets on the request itself.
		preset map[string]string
		// want is what the upstream must receive.
		want map[string]string
	}
	tests := []tc{
		{name: "no defaults and no presets", want: map[string]string{"X-Trace": ""}},
		{
			name:     "a default fills an unset header",
			defaults: map[string]string{"X-Trace": "sdk"},
			want:     map[string]string{"X-Trace": "sdk"},
		},
		{
			name:     "the caller's own value wins",
			defaults: map[string]string{"X-Trace": "sdk"},
			preset:   map[string]string{"X-Trace": "mine"},
			want:     map[string]string{"X-Trace": "mine"},
		},
		{
			name:     "several defaults, one overridden",
			defaults: map[string]string{"X-Trace": "sdk", "X-Tenant": "a"},
			preset:   map[string]string{"X-Tenant": "b"},
			want:     map[string]string{"X-Trace": "sdk", "X-Tenant": "b"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(chan http.Header, 1)
		srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			seen <- r.Header.Clone()
			w.WriteHeader(http.StatusOK)
		})
		client := newClient(t, srv, corenet.ClientConfig{DefaultHeaders: c.defaults})
		req, rerr := http.NewRequestWithContext(
			t.Context(), http.MethodGet, srv.URL+"/v1/subscribers", nil)
		if rerr != nil {
			t.Fatalf("build request: %v", rerr)
		}
		for k, v := range c.preset {
			req.Header.Set(k, v)
		}

		if _, err := client.Do(req); err != nil {
			t.Fatalf("Do = %v, want nil", err)
		}

		got := <-seen
		for k, want := range c.want {
			if got.Get(k) != want {
				t.Errorf("the upstream received %s = %q, want %q", k, got.Get(k), want)
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

// TestClient_Do pins that the typed entry point is guarded on
// exactly the same terms as the escape hatch — it goes through the same
// transport, so there is no second set of rules to keep in step.
func TestClient_Do(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// method and path are what the caller builds.
		method string
		path   string
		// wantCode is the refusal, or zero when the request is admitted.
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a listed read", method: http.MethodGet, path: "/v1/subscribers"},
		{name: "a write verb", method: http.MethodPut, path: "/v1/subscribers", wantCode: corenet.CodeRequestDenied},
		{name: "a denied path", method: http.MethodGet, path: "/v1/oam/status", wantCode: corenet.CodeRequestDenied},
		{name: "an encoded separator", method: http.MethodGet, path: "/v1/supi/a%2fb", wantCode: corenet.CodeUnsafePath},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		srv, reached := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		client := newClient(t, srv, corenet.ClientConfig{})
		req, rerr := http.NewRequestWithContext(t.Context(), c.method, srv.URL+c.path, nil)
		if rerr != nil {
			t.Fatalf("build request: %v", rerr)
		}

		resp, err := client.Do(req)

		if c.wantCode == 0 {
			if err != nil {
				t.Fatalf("an admitted request failed: %v", err)
			}
			if resp.Status != http.StatusOK {
				t.Errorf("Status = %d, want 200", resp.Status)
			}
			return
		}
		if err == nil {
			t.Fatal("Do performed a request the policy forbids")
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("Do = %v, want code %v", err, c.wantCode)
		}
		//: a refusal must leave no trace on the network.
		if *reached {
			t.Error("the upstream was contacted despite the refusal")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestClient_Get_RefusesAForeignOrigin is the empirical form of the same
// property: the client must not reach a peer the caller never configured.
//
// "//host/path" is an AUTHORITY under RFC 3986, so resolving it against the base
// replaces the host — and the built-in policies judge only the method and the
// path, so an allowlist of "/v1/subscribers" happily authorises the request on
// its way to somewhere else entirely. A caller that passes a caller-supplied
// identifier into Get without thinking about it is the realistic route in.
//
// Two upstreams stand in for the two ends: the configured one, and the one a
// substitution would reach. The second must never be contacted at all.
func TestClient_Get_RefusesAForeignOrigin(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// target is what the caller passes to Get, with %s replaced by the
		// foreign upstream's authority.
		target string
	}
	tests := []tc{
		{name: "a scheme-relative reference", target: "//%s/v1/subscribers"},
		{name: "an absolute URL", target: "http://%s/v1/subscribers"},
		//: the same substitution with a query, which the client would encode
		//: onto the foreign request just as happily.
		{name: "a scheme-relative reference with a query", target: "//%s/v1/subscribers?a=b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		foreign, reached := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeOrFail(t, w, `{"secret":"reached the wrong peer"}`)
		})
		configured, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeOrFail(t, w, `{"ok":true}`)
		})
		client := newClient(t, configured, corenet.ClientConfig{})
		authority := strings.TrimPrefix(foreign.URL, "http://")

		_, err := client.Get(t.Context(), fmt.Sprintf(c.target, authority), nil)

		if err == nil {
			t.Fatal("the client resolved a path that carried its own origin")
		}
		if !errs.HasCode(err, corenet.CodeUnsafePath) {
			t.Fatalf("Get = %v, want UNSAFE_PATH", err)
		}
		//: the whole point: the other upstream is never contacted, not even a
		//: connection — a policy that judges paths cannot catch this one.
		if *reached {
			t.Fatal("the client reached an upstream the caller never configured, " +
				"past a policy that authorised only the path")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
