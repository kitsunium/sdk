// Package token — the JWS compact issuer.
package token

import (
	"strings"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// jwsIssuer mints JWS compact tokens under one algorithm-bound key.
type jwsIssuer struct {
	// bound is the signing key; its algorithm is fixed at construction.
	bound signingKey
	// header is the pre-rendered, pre-encoded JOSE header. It is built once
	// because it never varies: the algorithm cannot change without a new
	// issuer, which is the same reason there is no algorithm parameter.
	header string
	// cfg carries the stamping policy (iss, lifetime, typ, kid).
	cfg IssuerConfig
	// timeSource is the resolved clock, never nil.
	timeSource clock.Clock
}

// newJWSIssuer validates cfg and pre-renders the header.
func newJWSIssuer(bound signingKey, cfg IssuerConfig) (issuer coretoken.Issuer, err error) {
	//: a configuration that cannot be honoured is refused now (ADR 0031).
	if verr := validateIssuerConfig(cfg); verr != nil {
		//: propagate PolicyMisconfigured.
		return nil, verr
	}
	//: pre-encode once; the header is identical for every token this mints.
	return &jwsIssuer{
		bound: bound, cfg: cfg, timeSource: resolveClock(cfg.Clock),
		header: b64.EncodeToString([]byte(renderJOSEHeader(bound.algorithm(), cfg))),
	}, nil
}

// renderJOSEHeader builds the fixed header this issuer stamps on every token.
//
// It is rendered by hand rather than marshalled from a map: the members are
// known, the values are escaped, and a map marshal would re-encode identical
// bytes on every mint while sorting "alg" after "kid".
func renderJOSEHeader(alg coretoken.Algorithm, cfg IssuerConfig) string {
	typ := cfg.Type
	//: RFC 7519 §5.1's default; an explicit media type is better still.
	if typ == "" {
		//: the interoperable default.
		typ = defaultType
	}
	var builder strings.Builder
	builder.WriteString(`{"alg":`)
	builder.WriteString(quoteJSONString(alg.String()))
	//: kid is optional; an issuer publishing a JWK Set stamps one.
	if cfg.KeyID != "" {
		builder.WriteString(`,"kid":`)
		builder.WriteString(quoteJSONString(cfg.KeyID))
	}
	builder.WriteString(`,"typ":`)
	builder.WriteString(quoteJSONString(typ))
	builder.WriteByte('}')
	//: a complete JOSE header object.
	return builder.String()
}

// Issue renders claims as a JWS compact token.
func (i *jwsIssuer) Issue(claims coretoken.ClaimsValue) (token string, err error) {
	stamped, serr := stampIssuedClaims(claims, i.cfg, i.timeSource)
	//: refuses the eternal token unless the issuer opted in by name.
	if serr != nil {
		//: propagate ExpiryRequired.
		return "", serr
	}
	payload, cerr := encodeClaims(stamped, joseShape{})
	//: propagate IssueFailed.
	if cerr != nil {
		//: a claim the format cannot express.
		return "", cerr
	}
	//: header "." payload is the signing input, and also the token's prefix.
	input := i.header + "." + b64.EncodeToString(payload)
	signature, gerr := i.bound.sign([]byte(input))
	//: propagate IssueFailed.
	if gerr != nil {
		//: a signing fault.
		return "", gerr
	}
	//: the third segment completes the compact serialization.
	return input + "." + b64.EncodeToString(signature), nil
}
