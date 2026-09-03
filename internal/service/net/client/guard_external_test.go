package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/client"
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
func newClient(t *testing.T, srv *httptest.Server, cfg client.Config) *client.Client {
	t.Helper()
	cfg.BaseURL = srv.URL
	c, err := client.New(cfg, corenet.IdentityValue{}, buildReadOnly(t), nil)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// TestPolicyIsEnforcedInTheTransport is the security guarantee of this package.
//
// The refused request must not reach the upstream at all — and crucially, it
// must not reach it even when the caller bypasses the typed Client entirely and
// forges its own request through the raw *http.Client. That escape hatch is the
// realistic attack on this design: a third-party SDK handed the client, or a
// contributor who "just needs one more call". If the check lived in Do rather
// than in the RoundTripper, this test would fail.
func TestPolicyIsEnforcedInTheTransport(t *testing.T) {
	t.Parallel()
	srv, reached := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c := newClient(t, srv, client.Config{})

	//: forge a request through the raw client, deliberately bypassing Do.
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodDelete, srv.URL+"/v1/subscribers", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := c.HTTP().Do(req)
	//: an admitted write would mean the guarantee is a convention, not a fact.
	if err == nil {
		closeQuietly(t, resp.Body)
		t.Fatal("the raw client performed a request the policy forbids")
	}
	if !errs.HasCode(err, codeOf(t, corenet.RequestDenied)) {
		t.Fatalf("expected REQUEST_DENIED, got %v", err)
	}
	//: a refusal must leave no trace on the network, not even a connection.
	if *reached {
		t.Fatal("the upstream was contacted despite the refusal")
	}
}

// TestNilPolicyIsRefused pins that a client cannot be built without a policy: an
// unguarded egress path wearing the name of a guarded one is worse than none.
func TestNilPolicyIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := client.New(client.Config{}, corenet.IdentityValue{}, nil, nil); err == nil {
		t.Fatal("a client was built with no policy")
	}
}

// TestNoContentIsNotAnError pins that 204 with an empty body is a normal
// outcome. The SDM returns it for a subscriber with no session, so a client that
// treats "no body" as a failure breaks the nominal case.
func TestNoContentIsNotAnError(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, srv, client.Config{})
	resp, err := c.Get(context.Background(), "/v1/subscribers", nil)
	if err != nil {
		t.Fatalf("204 reported as an error: %v", err)
	}
	if resp.Status != http.StatusNoContent {
		t.Fatalf("Status = %d, want 204", resp.Status)
	}
	if len(resp.Body) != 0 {
		t.Fatalf("Body = %q, want empty", resp.Body)
	}
}

// TestNonSuccessReturnsBodyAndError pins that a 4xx yields BOTH the response and
// an error: a caller diagnosing the failure needs the body, and a caller who
// never thought about the status still gets an error.
func TestNonSuccessReturnsBodyAndError(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeOrFail(t, w, `{"detail":"enable and disable are exclusive"}`)
	})
	c := newClient(t, srv, client.Config{})
	resp, err := c.Get(context.Background(), "/v1/subscribers", url.Values{"enable": {"true"}})
	if err == nil {
		t.Fatal("a 400 was reported as success")
	}
	if resp.Status != http.StatusBadRequest {
		t.Fatalf("Status = %d, want 400", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "exclusive") {
		t.Fatalf("the body was not returned alongside the error: %q", resp.Body)
	}
}

// TestOversizedBodyFailsRatherThanTruncating pins that the ceiling fails the
// call. A truncating version surfaced as a JSON decode error three layers away
// that named nothing useful.
func TestOversizedBodyFailsRatherThanTruncating(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeOrFail(t, w, string(make([]byte, 4096)))
	})
	c := newClient(t, srv, client.Config{MaxResponseSize: 128})
	_, err := c.Get(context.Background(), "/v1/subscribers", nil)
	if err == nil {
		t.Fatal("an oversized body was accepted")
	}
	if !errs.HasCode(err, codeOf(t, corenet.ResponseTooLarge)) {
		t.Fatalf("expected RESPONSE_TOO_LARGE, got %v", err)
	}
}

// TestQueryIsEncodedByTheClient pins that a caller never escapes a query value
// by hand — one forgotten escape is all it takes.
func TestQueryIsEncodedByTheClient(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 1)
	srv, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	})
	c := newClient(t, srv, client.Config{})
	_, err := c.Get(context.Background(), "/v1/subscribers", url.Values{"profileId": {"P tenant/a"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := <-seen; got != "profileId=P+tenant%2Fa" {
		t.Fatalf("query = %q, want the escaped form", got)
	}
}

// TestCallHookReportsTheCall pins the observation contract: exactly one record
// per outbound call, carrying the status, the path and a measured duration.
func TestCallHookReportsTheCall(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeOrFail(t, w, "0123456789")
	})
	calls := make(chan corenet.CallValue, 4)
	c, err := client.New(client.Config{BaseURL: srv.URL}, corenet.IdentityValue{},
		buildReadOnly(t), func(call corenet.CallValue) { calls <- call })
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, gerr := c.Get(context.Background(), "/v1/subscribers", nil); gerr != nil {
		t.Fatalf("unexpected error: %v", gerr)
	}
	close(calls)
	observed := drain(calls)
	if len(observed) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(observed))
	}
	if observed[0].Status != http.StatusOK || observed[0].Path != "/v1/subscribers" {
		t.Fatalf("hook saw %+v", observed[0])
	}
	if observed[0].Duration <= 0 {
		t.Fatal("hook reported a non-positive duration")
	}
}

// TestRefusedCallIsObservedWithoutAStatus pins that a refusal is reported to the
// hook too — an egress that never happened still belongs in the audit trail.
func TestRefusedCallIsObservedWithoutAStatus(t *testing.T) {
	t.Parallel()
	srv, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	calls := make(chan corenet.CallValue, 4)
	c, err := client.New(client.Config{BaseURL: srv.URL}, corenet.IdentityValue{},
		buildReadOnly(t), func(call corenet.CallValue) { calls <- call })
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, gerr := c.Get(context.Background(), "/v1/authentication/all", nil); gerr == nil {
		t.Fatal("a denied path was admitted")
	}
	close(calls)
	observed := drain(calls)
	if len(observed) != 1 {
		t.Fatalf("hook fired %d times, want 1", len(observed))
	}
	if observed[0].Status != 0 {
		t.Fatalf("a refused call reported status %d, want none", observed[0].Status)
	}
	if observed[0].Err == nil {
		t.Fatal("a refused call reported no error")
	}
}

// drain collects every value buffered in a closed channel.
func drain(ch <-chan corenet.CallValue) []corenet.CallValue {
	out := make([]corenet.CallValue, 0, len(ch))
	for call := range ch {
		out = append(out, call)
	}
	return out
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
