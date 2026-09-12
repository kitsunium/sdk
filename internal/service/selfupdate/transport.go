// Package selfupdate — the HTTP policy every real Service uses: bounded,
// https-only redirects and a cap on how much of a response is read.
// Package updater — the transport the release endpoints are reached over,
// and the bound on what they are allowed to make this process do.
//
// Everything here treats the GitHub API and the release CDN as untrusted
// input, because they are: stable releases resolve through a PUBLIC mirror
// repository (see repoForTag), which is a second publishing origin, and a
// redirect chain or a response body is chosen entirely by whatever answers.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// Transport bounds.
const (
	// maxRedirects caps the redirect chain. GitHub answers a release asset
	// download with one redirect to objects.githubusercontent.com, so a
	// handful of hops is generous; Go's default of 10 is not a bound anyone
	// chose for this.
	maxRedirects int = 5
	// maxAPIBodyBytes caps a GitHub REST response. The archive path was
	// already capped (maxArchiveBytes) while the JSON path was not, so a
	// hostile or broken endpoint could stream unbounded bytes into
	// json.Decoder. A /releases page is a few hundred kilobytes.
	maxAPIBodyBytes int64 = 8 << 20
)

// newReleaseHTTPClient builds the client every real (non-injected) Service
// uses.
//
// Go's DEFAULT redirect policy allows ten hops AND allows https→http: the
// standard library only strips sensitive headers on a cross-host redirect,
// it never refuses a scheme downgrade. On this path that would put a release
// archive — code about to be given 0755 and moved over the running binary —
// on a plaintext connection chosen by the redirect target.
//
// pkg/license/ci.go solves the same problem by refusing redirects OUTRIGHT,
// and that is right for the endpoint it talks to: the Actions token minter
// has no legitimate redirect, so the safe answer and the correct one
// coincide. Here they do not — `github.com/.../releases/download/...` always
// redirects to the asset CDN — so the policy is the closest equivalent that
// still works: follow, but only ever to https, and only a bounded number of
// times.
func newReleaseHTTPClient() *http.Client {
	//: Same timeout as before; only the redirect policy is tightened.
	return &http.Client{
		Timeout:       httpTimeout,
		CheckRedirect: checkReleaseRedirect,
	}
}

// checkReleaseRedirect is newReleaseHTTPClient's policy, kept as a named
// function so the decision is testable without standing up a redirect chain.
func checkReleaseRedirect(req *http.Request, via []*http.Request) error {
	//: net/http never passes a nil request, but a nil deref here would take
	//: the process down inside a transport callback — refuse instead.
	if req == nil || req.URL == nil {
		//: Nothing to vouch for.
		return fmt.Errorf("%w: redirect target is unreadable", coreupd.InsecureRedirect)
	}
	//: A chain longer than the cap is a loop or a deliberate amplification.
	if len(via) >= maxRedirects {
		//: Stop following.
		return fmt.Errorf("%w: more than %d redirects (last: %s)", coreupd.InsecureRedirect, maxRedirects, req.URL.Redacted())
	}
	//: A scheme downgrade puts the payload on a plaintext connection the
	//: redirect target chose. Refuse it whatever the host is.
	if req.URL.Scheme != "https" {
		//: Stop following.
		return fmt.Errorf("%w: redirect to %s (scheme %q)", coreupd.InsecureRedirect, req.URL.Redacted(), req.URL.Scheme)
	}
	//: A hop worth following.
	return nil
}

// decodeJSONBody reads at most capBytes from body and unmarshals it into
// `into`, refusing anything larger rather than letting an untrusted endpoint
// choose how much memory this process spends.
//
// json.Decoder over a raw response body — what every call site used before —
// has no such bound: it streams until EOF. The cap is a parameter rather
// than a constant read inside so the refusal branch is reachable in a test
// without producing an 8MB fixture.
func decodeJSONBody(body io.Reader, capBytes int64, into any) error {
	//: Read one byte past the cap so an oversized stream is detectable
	//: without unbounded allocation — same shape as bufferArchive.
	raw, err := io.ReadAll(io.LimitReader(body, capBytes+1))
	//: Propagate stream read failures to the caller's phase wrapper.
	if err != nil {
		//: Wrap to identify the read phase in operator logs.
		return fmt.Errorf("reading release API response: %w", err)
	}
	//: Refuse a body beyond the cap instead of decoding a truncated prefix.
	if int64(len(raw)) > capBytes {
		//: Raise the sentinel so callers can errors.Is the size refusal.
		return fmt.Errorf("%w: exceeds %d bytes", coreupd.APIBodyTooLarge, capBytes)
	}
	//: Decode the bounded body; the caller wraps with its own context.
	return json.Unmarshal(raw, into)
}
