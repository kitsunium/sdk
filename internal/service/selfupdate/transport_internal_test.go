// Package updater provides self-update functionality for ktn-linter binary.
// White-box tests for the transport bounds: what a release endpoint is
// allowed to redirect this client into, and how much of a response body it
// is allowed to make this process hold.
package selfupdate

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// requestTo builds a redirect target request for checkReleaseRedirect.
func requestTo(t *testing.T, raw string) *http.Request {
	t.Helper()
	parsed, err := url.Parse(raw)
	//: A malformed fixture URL would test nothing.
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return &http.Request{URL: parsed}
}

// Test_checkReleaseRedirect pins the policy: https only, bounded hops.
//
// Go's DEFAULT policy allows both of the refusals below — ten hops, and an
// https→http downgrade (the standard library strips sensitive headers on a
// cross-HOST redirect, never on a scheme change). On a path that ends in
// chmod 0755 over the running binary, neither is a bound anyone chose.
func Test_checkReleaseRedirect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		//: target is the redirect destination; empty means a nil request.
		target string
		//: hops is how many redirects have already been followed.
		hops      int
		wantErrIs error
	}{
		{name: "first https hop is followed", target: "https://objects.githubusercontent.com/a", hops: 1},
		{name: "last permitted hop is followed", target: "https://objects.githubusercontent.com/a", hops: maxRedirects - 1},
		{
			name:      "downgrade to http is refused",
			target:    "http://objects.githubusercontent.com/a",
			hops:      1,
			wantErrIs: coreupd.InsecureRedirect,
		},
		{
			name:      "same-host downgrade is refused too",
			target:    "http://github.com/kodflow/ktn/releases/download/v1/x.tar.gz",
			hops:      1,
			wantErrIs: coreupd.InsecureRedirect,
		},
		{
			name:      "non-http scheme is refused",
			target:    "file:///etc/passwd",
			hops:      1,
			wantErrIs: coreupd.InsecureRedirect,
		},
		{
			name:      "hop cap is enforced",
			target:    "https://objects.githubusercontent.com/a",
			hops:      maxRedirects,
			wantErrIs: coreupd.InsecureRedirect,
		},
		{name: "nil request is refused", target: "", wantErrIs: coreupd.InsecureRedirect},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var req *http.Request
			//: An empty target stands for the nil-request defence.
			if tc.target != "" {
				req = requestTo(t, tc.target)
			}
			via := make([]*http.Request, tc.hops)

			err := checkReleaseRedirect(req, via)
			//: Refusal rows: classify the sentinel.
			if tc.wantErrIs != nil {
				if !errors.Is(err, tc.wantErrIs) {
					t.Errorf("checkReleaseRedirect() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				return
			}
			//: Followed rows: the hop is permitted silently.
			if err != nil {
				t.Errorf("checkReleaseRedirect() unexpected error = %v", err)
			}
		})
	}
}

// TestNewReleaseHTTPClient_refusesDowngrade is the end-to-end proof that the
// policy is actually installed on the client the production constructor
// builds — not merely defined next to it.
//
// A test server answers with a redirect to a non-https URL, which is exactly
// the shape a compromised or misconfigured release CDN would produce. Under
// Go's DEFAULT policy this request succeeds: the standard library strips
// sensitive headers on a cross-host redirect but never refuses a scheme
// downgrade. The assertion is that this client does.
//
// The rows are different schemes on purpose. The guard is written as
// `scheme != "https"` rather than `scheme == "http"`, and only a second scheme
// distinguishes the two spellings — an allowlist refuses ftp, a denylist
// follows it and hands a release archive to whatever answers.
func TestNewReleaseHTTPClient_refusesDowngrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		why    string
	}{
		{
			name:   "https to http",
			target: "http://127.0.0.1:1/asset.tar.gz",
			why:    "the payload is about to be given 0755 and moved over the running binary",
		},
		{
			name:   "https to a wholly different scheme",
			target: "ftp://127.0.0.1:1/asset.tar.gz",
			why:    "the guard must be an https allowlist, not an http denylist",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: The destination is never contacted — the policy refuses before
			//: the connection — so an unroutable port keeps this off the
			//: network entirely.
			//: A real listener on purpose: the request below goes through
			//: newReleaseHTTPClient(), the production client, which cannot
			//: reach an in-memory server. Exercising the real transport is the
			//: point of this test.
			downgrade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Redirect(w, &http.Request{}, tc.target, http.StatusFound)
			}))
			t.Cleanup(downgrade.Close)

			client := newReleaseHTTPClient()
			resp, err := client.Get(downgrade.URL)
			//: A followed redirect would return a response instead of an error.
			if err == nil {
				closeBestEffort(resp.Body, "downgrade response")
				t.Fatalf("Get() followed a redirect to %s, want it refused (%s)", tc.target, tc.why)
			}
			//: net/http wraps CheckRedirect's error in *url.Error; errors.Is
			//: must still classify it or license_gate.go cannot report the
			//: real cause.
			if !errors.Is(err, coreupd.InsecureRedirect) {
				t.Errorf("Get() error = %v, want errors.Is coreupd.InsecureRedirect", err)
			}
		})
	}
}

// TestNewReleaseHTTPClient_followsSecureRedirect is the other half: the
// policy must not have broken the real download path, which ALWAYS redirects
// (github.com/.../releases/download/... -> the asset CDN).
//
// The rows walk chain lengths up to the cap. A hop count is the kind of bound
// that is easy to get wrong by one in the RESTRICTIVE direction, and that
// mistake is invisible from the refusal side: refusing one hop too early still
// looks like "the cap works" while making every real download fail.
//
// The boundary is maxRedirects-1, not maxRedirects. checkReleaseRedirect
// refuses when len(via) >= maxRedirects, and `via` already holds the requests
// made so far, so the maxRedirects-th redirect is the one refused and a chain
// of maxRedirects-1 hops is the longest that completes. Pinning that number
// here is what stops the cap drifting by one in either direction unnoticed.
func TestNewReleaseHTTPClient_followsSecureRedirect(t *testing.T) {
	t.Parallel()

	const payload string = "asset bytes"

	tests := []struct {
		name string
		hops int
	}{
		{name: "the single hop a real download makes", hops: 1},
		{name: "two hops", hops: 2},
		{name: "the longest chain inside the cap", hops: maxRedirects - 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Real TLS listeners on purpose: the redirect chain below is
			//: followed by the production client, and the hop count is what
			//: this test measures.
			asset := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if _, err := w.Write([]byte(payload)); err != nil {
					t.Logf("response Write: %v", err)
				}
			}))
			t.Cleanup(asset.Close)

			//: One TLS origin that keeps redirecting to itself with a
			//: decreasing counter builds a chain of any length without
			//: standing up one server per hop.
			var origin *httptest.Server
			origin = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				remaining, convErr := strconv.Atoi(r.URL.Query().Get("hops"))
				//: An absent or unparseable counter means this is the last
				//: hop; failing the handler would hide the real assertion
				//: behind a transport error.
				if convErr != nil {
					t.Logf("hops parameter %q: %v — treating as the final hop", r.URL.Query().Get("hops"), convErr)
				}
				//: The last hop lands on the asset.
				if convErr != nil || remaining <= 1 {
					http.Redirect(w, r, asset.URL, http.StatusFound)
					return
				}
				http.Redirect(w, r, origin.URL+"?hops="+strconv.Itoa(remaining-1), http.StatusFound)
			}))
			t.Cleanup(origin.Close)

			client := newReleaseHTTPClient()
			//: Trust the test servers' self-signed certificates while keeping
			//: the redirect policy under test — that is the behaviour pinned.
			client.Transport = asset.Client().Transport

			resp, err := client.Get(origin.URL + "?hops=" + strconv.Itoa(tc.hops))
			//: An https->https chain inside the cap is legitimate.
			if err != nil {
				t.Fatalf("Get() over %d hop(s) error = %v, want the chain followed", tc.hops, err)
			}
			t.Cleanup(func() { closeBestEffort(resp.Body, "asset response") })

			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				t.Fatalf("read body: %v", readErr)
			}
			//: Reaching the asset is the point; a refusal would have returned
			//: an error above, and a wrong body would mean a broken chain.
			if string(body) != payload {
				t.Errorf("body = %q, want %q", body, payload)
			}
		})
	}
}

// Test_decodeJSONBody pins the read cap the GitHub API path was missing: the
// archive was already bounded at 256MB while json.Decoder streamed the API
// response until EOF, letting the endpoint choose the allocation.
func Test_decodeJSONBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		//: capBytes is a parameter precisely so the refusal branch is
		//: reachable without an 8MB fixture.
		capBytes        int64
		wantTag         string
		wantErrIs       error
		wantErrContains string
	}{
		{
			name:     "body within the cap decodes",
			body:     `{"tag_name":"v1.2.3"}`,
			capBytes: 1024,
			wantTag:  "v1.2.3",
		},
		{
			name:     "body exactly at the cap decodes",
			body:     `{"tag_name":"v1"}`,
			capBytes: int64(len(`{"tag_name":"v1"}`)),
			wantTag:  "v1",
		},
		{
			name:      "body past the cap is refused, not truncated",
			body:      `{"tag_name":"v1.2.3"}`,
			capBytes:  4,
			wantErrIs: coreupd.APIBodyTooLarge,
		},
		{
			name:            "malformed json is reported",
			body:            `{"tag_name":`,
			capBytes:        1024,
			wantErrContains: "unexpected end of JSON input",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var release releaseInfo
			err := decodeJSONBody(strings.NewReader(tc.body), tc.capBytes, &release)

			//: Error rows: classify the sentinel or the message.
			if tc.wantErrIs != nil || tc.wantErrContains != "" {
				if err == nil {
					t.Fatal("decodeJSONBody() error = nil, want error")
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("decodeJSONBody() error = %v, want errors.Is %v", err, tc.wantErrIs)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("decodeJSONBody() error = %q, want substring %q", err.Error(), tc.wantErrContains)
				}
				//: A refused body must leave the destination untouched.
				if release.TagName != "" {
					t.Errorf("decodeJSONBody() populated TagName = %q on failure", release.TagName)
				}
				return
			}

			//: Happy rows: the bounded body decoded.
			if err != nil {
				t.Fatalf("decodeJSONBody() unexpected error = %v", err)
			}
			if release.TagName != tc.wantTag {
				t.Errorf("decodeJSONBody() TagName = %q, want %q", release.TagName, tc.wantTag)
			}
		})
	}
}

// Test_decodeJSONBody_readError pins that a failing reader is reported with
// its phase rather than silently decoding a partial body.
//
// The mid-body row is the one that matters. A stream that fails after emitting
// syntactically incomplete JSON is indistinguishable, at the json.Unmarshal
// call, from an endpoint that returned malformed JSON — and those two need
// opposite responses: one is a transient network fault worth retrying, the
// other is a broken or hostile endpoint. Naming the read phase is what keeps
// them apart in an operator's log.
func Test_decodeJSONBody_readError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body io.Reader
		why  string
	}{
		{
			name: "the read fails immediately",
			body: failingReader{},
			why:  "nothing was received at all",
		},
		{
			name: "the read fails after a partial body",
			body: io.MultiReader(strings.NewReader(`{"tag_name":"v1.0`), failingReader{}),
			why:  "a truncated body must not be reported as malformed JSON",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var release releaseInfo
			err := decodeJSONBody(tc.body, maxAPIBodyBytes, &release)
			//: A transport failure mid-body must not look like malformed JSON.
			if err == nil {
				t.Fatalf("decodeJSONBody() error = nil, want the read failure surfaced (%s)", tc.why)
			}
			if !strings.Contains(err.Error(), "reading release API response") {
				t.Errorf("decodeJSONBody() error = %q, want the read phase named (%s)", err.Error(), tc.why)
			}
			//: A partial body must not have been decoded into the target.
			if release.TagName != "" {
				t.Errorf("decodeJSONBody() populated TagName = %q from a failed read", release.TagName)
			}
		})
	}
}

// TestService_getLatestVersion_refusesOversizedBody proves the cap is wired
// into the real API call, not merely available next to it.
//
// Both rows are needed and neither is filler. The oversized row shows the
// endpoint does not get to choose how much this process allocates; the ordinary
// row shows the cap did not simply break the API path, which is the failure
// mode a bound introduced defensively actually has — a refusal that fires on
// every response looks identical to a working guard until someone tries to
// upgrade.
func TestService_getLatestVersion_refusesOversizedBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		oversized bool
		wantTag   string
	}{
		{name: "a body past the cap is refused", oversized: true},
		{name: "an ordinary release document is decoded", oversized: false, wantTag: "v9.9.9"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				//: The ordinary row is a few dozen bytes; only the refusal row
				//: needs to stream past the cap.
				if !tc.oversized {
					if _, err := w.Write([]byte(`{"tag_name":"v9.9.9"}`)); err != nil {
						t.Logf("response Write: %v", err)
					}
					return
				}
				//: One byte past the cap, produced by a streaming write so the
				//: fixture is not held in the test's own memory twice.
				chunk := make([]byte, 1<<16)
				written := int64(0)
				for written <= maxAPIBodyBytes {
					n, err := w.Write(chunk)
					written += int64(n)
					//: A client that has already given up closes the connection.
					if err != nil {
						return
					}
				}
			}))
			//: In-memory network: no real port. The server starts on the first
			//: Client() call, which is also what fills server.URL, so start it
			//: here and let the reads below run in any order. NewTestServer
			//: registers the cleanup itself.
			server.Client()

			client := &http.Client{Transport: &mockTransport{url: server.URL, client: server.Client()}}
			svc := NewUpdaterWithDeps("v1.0.0", testSource, client, &mockFileSystem{}, &mockCopier{})

			got, err := svc.getLatestVersion()
			//: The endpoint does not get to choose how much this process allocates.
			if tc.oversized {
				if !errors.Is(err, coreupd.APIBodyTooLarge) {
					t.Errorf("getLatestVersion() error = %v, want errors.Is coreupd.APIBodyTooLarge", err)
				}
				return
			}
			//: ...and a normal-sized response must still be decoded.
			if err != nil {
				t.Fatalf("getLatestVersion() error = %v, want an ordinary body accepted", err)
			}
			if got != tc.wantTag {
				t.Errorf("getLatestVersion() = %q, want %q", got, tc.wantTag)
			}
		})
	}
}
