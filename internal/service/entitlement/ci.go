// Package entitlement - obtaining the proof that a run is CI.
//
// The token itself is fetched from the runner, which means the URL and the
// bearer credential both arrive as environment variables — attacker-controlled
// input by the same standard as everything else this package reads. They are
// validated before use rather than trusted because of where they came from.
package entitlement

import (
	"crypto/rsa"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// Environment variables the Actions runner injects when a workflow declares
// `permissions: id-token: write`.
const (
	// actionsTokenURLEnv holds the endpoint that mints an OIDC token.
	actionsTokenURLEnv string = "ACTIONS_ID_TOKEN_REQUEST_URL"
	// actionsTokenBearerEnv holds the ephemeral credential authorising that
	// request. It is what someone outside Actions cannot obtain.
	actionsTokenBearerEnv string = "ACTIONS_ID_TOKEN_REQUEST_TOKEN"
)

// actionsTokenHostSuffix is the only host family the request URL may name.
//
// The URL arrives as an environment variable, so without this the bearer
// credential would be sent wherever that variable pointed — handing an
// attacker the one secret the runner has, by setting a variable.
const actionsTokenHostSuffix string = ".actions.githubusercontent.com"

// maxTokenResponseBytes caps the mint endpoint's response. A token is a couple
// of kilobytes; anything past this is not one.
const maxTokenResponseBytes int64 = 64 << 10

// InCI reports whether this process can even attempt to prove it is a CI run.
//
// It is deliberately NOT an entitlement check: both variables being present
// means only that a token could be requested. Anyone can set them, which is
// exactly why proving anything requires the signature that follows.
func InCI() bool {
	//: Both are needed; one without the other mints nothing.
	return os.Getenv(actionsTokenURLEnv) != "" && os.Getenv(actionsTokenBearerEnv) != ""
}

// checkTokenURL refuses a mint endpoint that is not GitHub's.
//
// The URL is environment-supplied and the request carries a bearer credential,
// so an unchecked one is a way to exfiltrate that credential to any host by
// setting a variable. https and a githubusercontent host are both required.
func checkTokenURL(raw string) (parsed *url.URL, err error) {
	parsed, parseErr := url.Parse(raw)
	//: A URL we cannot parse cannot be checked, so it cannot be used.
	if parseErr != nil {
		//: Refuse the malformed endpoint.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_token_url"),
			errs.String("condition", "the runner named a url that does not parse"))
	}
	//: Plaintext would put the bearer credential on the wire.
	if parsed.Scheme != "https" {
		//: Refuse anything but https.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_token_url"),
			errs.String("condition", "the scheme would put the credential on the wire in clear"),
			errs.String("scheme", parsed.Scheme))
	}
	host := parsed.Hostname()
	//: Suffix match on a dotted prefix, never Contains: "evil-actions.
	//: githubusercontent.com.attacker.test" contains the string and is not
	//: GitHub, while a bare "actions.githubusercontent.com" is.
	if host != strings.TrimPrefix(actionsTokenHostSuffix, ".") &&
		!strings.HasSuffix(host, actionsTokenHostSuffix) {
		//: Refuse to send the credential anywhere else.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "check_token_url"),
			errs.String("condition", "the host is not GitHub's token endpoint"),
			errs.String("host", host))
	}
	//: An endpoint worth handing the runner's credential to.
	return parsed, nil
}

// RequestActionsToken exchanges the runner's credentials for an OIDC token.
//
// The audience is ours and is set here rather than taken from anywhere: a
// workflow must not be able to ask for a token this verifier will then accept
// for a purpose it never agreed to.
func RequestActionsToken(get BearerFetch, audience string) (token string, err error) {
	rawURL, bearer := os.Getenv(actionsTokenURLEnv), os.Getenv(actionsTokenBearerEnv)
	//: Nothing to exchange means this is not an Actions run at all.
	if rawURL == "" || bearer == "" {
		//: Report it as unprovable, never as a refusal: the caller falls back
		//: to the device path rather than treating this as a denial.
		return "", refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "mint_token"),
			errs.String("condition", "neither runner token variable is set"))
	}

	endpoint, urlErr := checkTokenURL(rawURL)
	//: An endpoint we will not vouch for is one we will not authenticate to.
	if urlErr != nil {
		//: Propagate the endpoint failure.
		return "", urlErr
	}
	//: Build the query rather than concatenate: an audience with a "&" in it
	//: would otherwise inject a second parameter.
	query := endpoint.Query()
	query.Set("audience", audience)
	endpoint.RawQuery = query.Encode()

	raw, fetchErr := fetchToken(get, endpoint.String(), bearer)
	//: A token we could not fetch proves nothing.
	if fetchErr != nil {
		//: Propagate the transport failure.
		return "", fetchErr
	}
	//: Return the compact JWS for verification; nothing here trusts it.
	return raw, nil
}

// tokenResponseBody performs the mint request and returns its body, refusing
// every answer that is not one a token could be read from.
func tokenResponseBody(get BearerFetch, endpoint, bearer string) (body []byte, err error) {
	//: A caller with no way to send the credential cannot mint a token, and
	//: an unauthenticated request would only ever return 401.
	if get == nil {
		//: Refuse rather than make a request that cannot succeed.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "mint_token"),
			errs.String("condition", "no BearerFetch was supplied"))
	}

	resp, getErr := get(endpoint, bearer)
	//: A transport failure means no proof is available.
	if getErr != nil {
		//: Report it as unprovable.
		//: classifyForeign: BearerFetch is injected through WithBearerFetch,
		//: so an errs-typed error of the consumer's own would otherwise
		//: replace the CI classification a caller's dispatch keys on.
		return nil, classifyForeign(coreent.ErrCIUnverifiable, getErr,
			errs.String("stage", "mint_token"))
	}
	//: A nil response with no error breaks the http contract, but panicking
	//: on it would take the whole process down over a CI seat — and this path
	//: must never do worse than fall back to a device.
	if resp == nil || resp.Body == nil {
		//: Report it as unprovable.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "mint_token"),
			errs.String("condition", "the transport returned neither a response nor an error"))
	}
	defer closeBestEffort(resp.Body, endpoint)

	//: Anything but 200 is an unusable answer, including the 403 a workflow
	//: without `id-token: write` gets.
	if resp.StatusCode != http.StatusOK {
		//: Report the refusal to mint.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "mint_token"),
			errs.String("condition", "the mint endpoint refused"),
			errs.Int("status", resp.StatusCode))
	}

	read, readErr := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes+1))
	//: A truncated body cannot be parsed.
	if readErr != nil {
		//: Report the transport failure.
		//: Same seam: the Body came back from the injected BearerFetch.
		return nil, classifyForeign(coreent.ErrCIUnverifiable, readErr,
			errs.String("stage", "read_token_response"))
	}
	//: A body at the cap is not a token response; an untrusted endpoint must
	//: not choose how much memory we spend.
	if int64(len(read)) > maxTokenResponseBytes {
		//: Refuse the oversized response.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "read_token_response"),
			errs.String("condition", "body at or past the cap"),
			errs.Int64("limit_bytes", maxTokenResponseBytes),
			errs.Int("got_bytes", len(read)))
	}
	//: A body worth decoding, still entirely untrusted.
	return read, nil
}

// fetchToken performs the mint request and extracts the token from the reply.
func fetchToken(get BearerFetch, endpoint, bearer string) (token string, err error) {
	body, bodyErr := tokenResponseBody(get, endpoint, bearer)
	//: An answer we could not obtain carries no token.
	if bodyErr != nil {
		//: Propagate the transport failure.
		return "", bodyErr
	}

	minted, decodeErr := strictUnmarshal[actionsTokenResponse](body, "token response")
	//: A response we cannot decode is not one.
	if decodeErr != nil {
		//: Propagate the decode failure.
		return "", decodeErr
	}
	//: An empty value is a well-formed response carrying no token.
	if minted.Value == "" {
		//: Refuse rather than verify an empty string.
		return "", refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "mint_token"),
			errs.String("condition", "a well-formed response carrying no token"))
	}
	//: Return the token, still entirely unverified.
	return minted.Value, nil
}

// DefaultBearerFetch is the transport the mint request should use.
//
// It refuses redirects outright. checkTokenURL vouches for the URL it is
// GIVEN, and a redirect is a second destination nobody checked: Go's default
// client decides whether to forward an Authorization header by comparing
// HOSTS, not schemes, so a same-host https→http redirect would put the
// runner's credential on the wire in plaintext. There is no legitimate
// redirect on this endpoint, so the safe answer and the correct one coincide.
func DefaultBearerFetch(url, bearer string) (resp *http.Response, err error) {
	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			//: Refuse rather than follow: the destination was never checked.
			return refuse(coreent.ErrCIUnverifiable,
				errs.String("stage", "mint_token"),
				errs.String("condition", "the token endpoint redirected and the destination was never checked"))
		},
	}

	req, reqErr := http.NewRequest(http.MethodGet, url, nil)
	//: A request we cannot build is one we cannot send.
	if reqErr != nil {
		//: Report it as unprovable.
		return nil, classify(coreent.ErrCIUnverifiable, reqErr,
			errs.String("stage", "build_token_request"))
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	//: Return whatever the endpoint said; nothing here trusts it.
	return client.Do(req)
}

// actionsTokenResponse is what the mint endpoint returns.
type actionsTokenResponse struct {
	// Value is the compact JWS.
	Value string `json:"value"`
}

// VerifyCI proves this run is CI for an entitled account, or explains why not.
//
// keys are the published JWKS entries; roster is the already-authenticated
// roster. Both are passed in so this stays a pure decision: the caller owns
// the fetching, and every refusal here is a property of the token or of the
// roster rather than of the network.
//
// audience is the product's own OIDC audience. It is a parameter rather than a
// constant because an audience shared between two products lets a token minted
// for one satisfy the other's gate — see ProductValue.CIAudience.
func VerifyCI(get BearerFetch, keys map[string]*rsa.PublicKey, roster *coreent.RosterValue, audience string, now time.Time) (claims *ActionsClaimsValue, err error) {
	//: Nothing to prove outside Actions; the caller falls back to a device.
	if !InCI() {
		//: Report it as unprovable rather than refused.
		return nil, refuse(coreent.ErrCIUnverifiable,
			errs.String("stage", "verify_ci"),
			errs.String("condition", "neither runner token variable is set"))
	}
	//: The audience is ours, so a token minted for another service cannot be
	//: replayed here.
	raw, tokenErr := RequestActionsToken(get, audience)
	//: A token we could not obtain proves nothing.
	if tokenErr != nil {
		//: Propagate the mint failure.
		return nil, tokenErr
	}

	verified, verifyErr := VerifyActionsToken(raw, keys, audience, now)
	//: An unauthenticated token is worth exactly nothing here.
	if verifyErr != nil {
		//: Propagate the verification failure.
		return nil, verifyErr
	}
	//: Authentic is not entitled: the roster decides which accounts get a
	//: free seat, and it is the only document with the vendor's signature on
	//: it. GitHub's word is that the run is real, not that it is paid for.
	if _, entitlementErr := roster.CIEntitlementFor(verified.RepositoryOwnerID, now); entitlementErr != nil {
		//: Propagate the entitlement refusal.
		return nil, entitlementErr
	}
	//: A genuine CI run, for an account the vendor covers.
	return verified, nil
}
