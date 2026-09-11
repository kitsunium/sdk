// Package token — the PASETO v4.public issuer.
package token

import (
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// pasetoIssuer mints v4.public tokens.
type pasetoIssuer struct {
	// bound is the Ed25519 signing key, tagged AlgorithmPasetoV4Public.
	bound signingKey
	// cfg is the PASETO policy: footer and implicit assertion included.
	cfg PasetoIssuerConfig
	// issuing is the shared claim-stamping projection of cfg.
	issuing IssuerConfig
	// timeSource is the resolved clock, never nil. It is a clock.Clock — the
	// frozen read-only half of the time port (ADR 0039) — because issuing
	// stamps instants and never waits for one.
	timeSource clock.Clock
}

// Issue renders claims as a v4.public token.
func (i *pasetoIssuer) Issue(claims coretoken.ClaimsValue) (token string, err error) {
	stamped, serr := stampIssuedClaims(claims, i.issuing, i.timeSource)
	//: refuses the eternal token unless the issuer opted in by name.
	if serr != nil {
		//: propagate ExpiryRequired.
		return "", serr
	}
	payload, cerr := encodeClaims(stamped, pasetoShape{})
	//: PASETO refuses a multi-valued audience rather than truncating it.
	if cerr != nil {
		//: propagate IssueFailed.
		return "", cerr
	}
	//: the signature covers the header, the payload, the footer and the
	//: implicit assertion — injectively, so no byte can move between them.
	signature, gerr := i.bound.sign(preAuthEncode(
		[]byte(pasetoV4PublicHeader), payload, i.cfg.Footer, i.cfg.ImplicitAssertion))
	//: propagate IssueFailed.
	if gerr != nil {
		//: a signing fault.
		return "", gerr
	}
	body := b64.EncodeToString(append(payload, signature...))
	//: no footer, no fourth segment.
	if len(i.cfg.Footer) == 0 {
		//: the three-segment form.
		return pasetoV4PublicHeader + body, nil
	}
	//: the four-segment form.
	return pasetoV4PublicHeader + body + "." + b64.EncodeToString(i.cfg.Footer), nil
}
