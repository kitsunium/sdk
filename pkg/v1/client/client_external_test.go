package client_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/client"
	"github.com/kitsunium/sdk/pkg/v1/tlsid"
)

// readOnly assembles the read-only policy through the public surface only.
func readOnly(t *testing.T) client.Policy {
	t.Helper()
	allow, err := client.AllowPaths(`/v1/(subscribers|supi/[^/]+)`)
	if err != nil {
		t.Fatalf("AllowPaths: %v", err)
	}
	deny, err := client.DenyPaths(`/v1/authentication.*`)
	if err != nil {
		t.Fatalf("DenyPaths: %v", err)
	}
	return client.Policies(client.AllowMethods(http.MethodGet), deny, allow)
}

// TestFacadeBuildsAWorkingReadOnlyClient pins that the whole flow is reachable
// from pkg/v1 alone — a consumer cannot import internal/*, so anything missing
// from this surface is unusable in practice.
func TestFacadeBuildsAWorkingReadOnlyClient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, werr := io.WriteString(w, `{"subscribers":[]}`); werr != nil {
			t.Errorf("write response: %v", werr)
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{BaseURL: srv.URL}, tlsid.Identity{}, readOnly(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.Get(t.Context(), "/v1/subscribers", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("Status = %d, want 200", resp.Status)
	}
	if string(resp.Body) != `{"subscribers":[]}` {
		t.Fatalf("Body = %q", resp.Body)
	}
}

// TestFacadeSentinelsAreMatchable pins that a consumer can branch on a refusal
// without reaching into internal/*, which Go's firewall forbids anyway.
func TestFacadeSentinelsAreMatchable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{BaseURL: srv.URL}, tlsid.Identity{}, readOnly(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct {
		name string
		path string
		want error
	}{
		{name: "denied path", path: "/v1/authentication/all", want: client.RequestDenied},
		{name: "unlisted path", path: "/v1/context-data", want: client.RequestDenied},
		{
			// Get resolves the path against the base URL, and url.ResolveReference
			// applies RFC 3986 dot-segment removal — so "/v1/supi/.." has already
			// collapsed to "/v1/" by the time any policy sees it, and is refused
			// as an unlisted path. The refusal still happens; only the reason
			// differs. TestForgedDotSegmentIsRefused covers the path Go does NOT
			// normalise, which is the one that actually needs the guard.
			name: "dot segment collapses before the policy and is still refused",
			path: "/v1/supi/..", want: client.RequestDenied,
		},
		{
			// The encoded form is the one normalisation does NOT touch:
			// ResolveReference leaves EscapedPath as "/v1/supi/%2e%2e" while
			// url.URL.Path silently decodes it to "/v1/supi/..". A policy that
			// judged the decoded Path would see "/v1/supi/.." and ADMIT it,
			// because `[^/]+` matches "..". Judging the escaped form is what
			// makes this reachable by the dot-segment guard at all.
			name: "percent-encoded dot segment survives resolution and is refused",
			path: "/v1/supi/%2e%2e", want: client.UnsafePath,
		},
		{
			name: "uppercase percent-encoded dot segment is refused too",
			path: "/v1/supi/%2E%2E", want: client.UnsafePath,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, gerr := c.Get(t.Context(), tc.path, nil)
			if !errors.Is(gerr, tc.want) {
				t.Fatalf("errors.Is(err, %v) = false, got %v", tc.want, gerr)
			}
		})
	}
}

// TestForgedDotSegmentIsRefused pins the case the dot-segment guard exists for.
//
// url.Parse does NOT remove dot segments, so a request built directly from a
// full URL — rather than resolved against a base — carries "/v1/supi/.." to the
// wire verbatim. An anchored allow pattern would admit it, because `[^/]+`
// matches ".." perfectly well, and the upstream would then normalise it to a
// different resource. The guard refuses it before any pattern is tried.
func TestForgedDotSegmentIsRefused(t *testing.T) {
	t.Parallel()
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{BaseURL: srv.URL}, tlsid.Identity{}, readOnly(t), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequestWithContext(
		t.Context(), http.MethodGet, srv.URL+"/v1/supi/..", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if got := req.URL.EscapedPath(); got != "/v1/supi/.." {
		t.Fatalf("url.Parse normalised the path to %q — the premise of this test changed", got)
	}
	resp, err := c.HTTP().Do(req)
	if err == nil {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
		t.Fatal("a forged dot-segment path was admitted")
	}
	if !errors.Is(err, client.UnsafePath) {
		t.Fatalf("expected UnsafePath, got %v", err)
	}
	if reached {
		t.Fatal("the upstream was contacted despite the refusal")
	}
}

// TestNilPolicyRefusedThroughFacade pins that the closed-by-default rule holds
// at the public boundary too.
func TestNilPolicyRefusedThroughFacade(t *testing.T) {
	t.Parallel()
	if _, err := client.New(client.Config{}, tlsid.Identity{}, nil, nil); err == nil {
		t.Fatal("a client was built with no policy")
	}
}
