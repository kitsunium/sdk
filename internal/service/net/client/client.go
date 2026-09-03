// Package client is the outbound half of the SDK's network domain (ADR 0029):
// an HTTP client whose read-only posture is enforced by the transport rather
// than by the call site.
package client

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Client performs guarded outbound HTTP calls.
//
// Every request it sends — including one forged through the *http.Client that
// HTTP returns — passes through a transport that consults the policy first, so
// the client's posture cannot be bypassed by forgetting a check at a call site.
type Client struct {
	// http carries the guarded transport; handing it out keeps the guarantee.
	http *http.Client
	// base is the origin relative paths resolve against; may be nil.
	base *url.URL
	// headers are applied to requests that do not already set them.
	headers map[string]string
}

// New builds a client from cfg, the TLS identity, the policy and the hook.
//
// A nil policy is refused rather than defaulted to "allow everything": an
// unguarded egress path wearing the name of a guarded one is worse than none.
func New(cfg Config, id corenet.IdentityValue, policy corenet.Policy, hook corenet.CallHook) (c *Client, err error) {
	//: refuse instead of defaulting open.
	if policy == nil {
		//: a missing policy is a configuration error, not permission.
		return nil, errs.Wrap(corenet.RequestDenied, errs.WrapParams{},
			errs.String("why", "a policy is required"))
	}
	full := withDefaults(cfg)
	base, berr := parseBase(full.BaseURL)
	//: a malformed base URL must fail at construction, not per call.
	if berr != nil {
		//: surface INVALID_ADDRESS from the parser.
		return nil, berr
	}
	//: every request goes through the guard; there is no unguarded path.
	guarded := &guard{
		next:     newTransport(full, id),
		policy:   policy,
		maxBytes: full.MaxResponseSize,
		hook:     hook,
	}
	//: the assembled client is ready to use.
	return &Client{
		http: &http.Client{
			Transport:     guarded,
			Timeout:       full.TotalTimeout.Duration(),
			CheckRedirect: redirectLimiter(full.MaxRedirects),
		},
		base:    base,
		headers: full.DefaultHeaders,
	}, nil
}

// HTTP returns the underlying *http.Client.
//
// The policy still applies: it lives in the transport, so a third-party SDK
// handed this client cannot escape it. That is precisely why the check was put
// there rather than in Do.
func (c *Client) HTTP() *http.Client {
	//: the guarantee travels with the transport, so this is safe to hand out.
	return c.http
}

// Get issues a guarded GET against a path relative to the configured base URL.
// The query is encoded here so no call site has to remember to escape it.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (resp corenet.ResponseValue, err error) {
	target, terr := c.resolve(path, query)
	//: a path that cannot be resolved never reaches the network.
	if terr != nil {
		//: surface INVALID_ADDRESS from the resolver.
		return corenet.ResponseValue{}, terr
	}
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	//: request construction only fails on a malformed method or URL.
	if rerr != nil {
		//: report it as an address problem rather than leaking the stdlib text.
		return corenet.ResponseValue{}, errs.Wrap(corenet.InvalidAddress, errs.WrapParams{},
			errs.String("why", "the request could not be built"))
	}
	//: from here the guarded path is identical to Do's.
	return c.Do(req)
}

// Do sends req and returns a fully-read response.
//
// It returns a value rather than an *http.Response so the caller cannot leak a
// connection by forgetting to close the body, and so the byte count is known —
// which is what lets the observation hook report an accurate size.
func (c *Client) Do(req *http.Request) (resp corenet.ResponseValue, err error) {
	c.applyHeaders(req)
	raw, derr := c.http.Do(req)
	//: a refusal or a transport failure already carries its own typed code.
	if derr != nil {
		//: unwrap the *url.Error envelope so the domain code stays matchable.
		return corenet.ResponseValue{}, unwrapClientError(derr)
	}
	body, berr := io.ReadAll(raw.Body)
	cerr := raw.Body.Close()
	out := corenet.ResponseValue{Status: raw.StatusCode, Header: raw.Header, Body: body}
	//: an over-sized body fails here rather than arriving truncated.
	if berr != nil {
		//: the status is still reported so the caller can see what happened.
		return corenet.ResponseValue{Status: raw.StatusCode, Header: raw.Header}, berr
	}
	//: a close failure after a complete read cannot change the payload, but it
	//: does signal a connection the pool should not reuse, so it is not hidden.
	if cerr != nil {
		//: report it rather than let a broken connection look healthy.
		return out, errs.Wrap(corenet.CallFailed, errs.WrapParams{},
			errs.String("why", "the response body could not be closed"))
	}
	//: a non-2xx is returned WITH the response, so a caller can tolerate a
	//: specific status deliberately while one that never considered it still
	//: receives an error.
	if raw.StatusCode < http.StatusOK || raw.StatusCode >= http.StatusMultipleChoices {
		//: the body is what makes the failure diagnosable.
		return out, errs.Wrap(corenet.CallFailed, errs.WrapParams{},
			errs.Int("status", raw.StatusCode))
	}
	//: a 2xx with no body — 204, typically — is a normal outcome.
	return out, nil
}

// applyHeaders adds the configured defaults without overriding the caller.
func (c *Client) applyHeaders(req *http.Request) {
	//: a caller-set header always wins over a configured default.
	for key, value := range c.headers {
		//: only fill what the caller left unset.
		if req.Header.Get(key) == "" {
			req.Header.Set(key, value)
		}
	}
}

// resolve builds an absolute URL from a path relative to the base.
func (c *Client) resolve(path string, query url.Values) (target string, err error) {
	ref, perr := url.Parse(path)
	//: an unparseable path is a caller error, caught before any I/O.
	if perr != nil {
		//: report it as an address problem.
		return "", errs.Wrap(corenet.InvalidAddress, errs.WrapParams{},
			errs.String("why", "the path could not be parsed"))
	}
	//: encoding the query here is what stops each call site inventing its own.
	if len(query) > 0 {
		ref.RawQuery = query.Encode()
	}
	//: without a base the caller is expected to pass an absolute URL.
	if c.base == nil {
		//: the path is already absolute, so use it as given.
		return ref.String(), nil
	}
	//: ResolveReference also applies RFC 3986 dot-segment removal, so a literal
	//: "/a/.." collapses before any policy sees it; the encoded form does not,
	//: which is why the policy still checks for dot segments itself.
	return c.base.ResolveReference(ref).String(), nil
}

// parseBase validates the configured base URL.
func parseBase(raw string) (base *url.URL, err error) {
	//: an empty base means the caller passes absolute URLs.
	if raw == "" {
		//: nil means "the caller supplies absolute URLs".
		return nil, nil
	}
	parsed, perr := url.Parse(raw)
	//: a base that names no origin would silently yield relative requests the
	//: transport cannot send.
	if perr != nil || parsed.Scheme == "" || parsed.Host == "" {
		//: refuse at construction rather than on the first call.
		return nil, errs.Wrap(corenet.InvalidAddress, errs.WrapParams{},
			errs.String("why", "the base URL must be absolute"))
	}
	//: the base names a usable origin.
	return parsed, nil
}

// newTransport builds the per-phase-bounded transport.
func newTransport(cfg Config, id corenet.IdentityValue) *http.Transport {
	//: each phase is bounded separately so a slow peer is distinguishable from
	//: a large body, which a single overall timeout cannot tell apart.
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   cfg.DialTimeout.Duration(),
			KeepAlive: defaultKeepAlive,
		}).DialContext,
		TLSClientConfig:       id.ClientConfig(),
		TLSHandshakeTimeout:   cfg.HandshakeTimeout.Duration(),
		ResponseHeaderTimeout: cfg.ResponseTimeout.Duration(),
		ForceAttemptHTTP2:     true,
	}
}

// redirectLimiter caps the redirect chain.
//
// Every hop still re-enters the transport, so the policy authorises each one
// individually: a 302 cannot walk the client out of its allowed surface.
func redirectLimiter(maxRedirects int) func(*http.Request, []*http.Request) error {
	//: the closure captures the cap so http.Client can consult it per hop.
	return func(_ *http.Request, via []*http.Request) error {
		//: the chain is capped; each hop was already policy-checked.
		if len(via) >= maxRedirects {
			//: refuse to follow any further.
			return errs.Wrap(corenet.TooManyRedirects, errs.WrapParams{},
				errs.Int("limit", maxRedirects))
		}
		//: within budget, so the hop is allowed to proceed.
		return nil
	}
}

// unwrapClientError pulls the typed cause out of the *url.Error that
// http.Client wraps every transport failure in, so the caller sees the domain
// code rather than a stdlib envelope that also quotes the full URL.
func unwrapClientError(err error) error {
	wrapped, ok := errors.AsType[*url.Error](err)
	//: http.Client wraps every failure, including our own typed refusals;
	//: unwrapping keeps the domain code reachable by errors.Is.
	if ok && wrapped.Err != nil {
		//: hand back the cause the transport actually produced.
		return wrapped.Err
	}
	//: not a transport failure — pass it through untouched.
	return err
}
