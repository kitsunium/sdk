// Package token — PASETO v4.public: the constructors and the scheme gate.
//
// PASETO is the format that answers the question "what if the algorithm were
// not negotiable". Its header is a version and a purpose — "v4.public." —
// baked into the signed bytes through the pre-authentication encoding, so
// there is no "alg" field to substitute and algorithm confusion has nothing to
// grip. This implementation still binds the key type at construction, because
// the property should hold for the same reason on both formats rather than by
// accident on one of them.
//
// Only v4.public ships. See the package CLAUDE.md §PASETO for v4.local.
package token

import (
	"crypto/ed25519"
	"slices"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// pasetoV4PublicHeader is the version+purpose prefix, INCLUDING the
	// trailing separator, exactly as it is fed to the pre-authentication
	// encoding. Signing the header is what stops a v4.public token from being
	// replayed as any other version or purpose.
	pasetoV4PublicHeader string = "v4.public."
	// pasetoMinSegments is a v4.public token with no footer: "v4", "public",
	// payload.
	pasetoMinSegments int = 3
	// pasetoMaxSegments adds the optional footer.
	pasetoMaxSegments int = 4
	// pasetoVersionSegment indexes the version part ("v4").
	pasetoVersionSegment int = 0
	// pasetoPurposeSegment indexes the purpose part ("public").
	pasetoPurposeSegment int = 1
	// pasetoBodySegment indexes the payload+signature part.
	pasetoBodySegment int = 2
	// pasetoFooterSegment indexes the optional footer part.
	pasetoFooterSegment int = 3
	// pasetoSigLen is the Ed25519 signature width appended to the payload.
	pasetoSigLen int = ed25519.SignatureSize
	// maxFooterLen bounds a decoded footer. A footer carries a key id or a
	// small hint; it is not a second payload.
	maxFooterLen int = 1 << 10
)

// pasetoVersions is the closed set of versions the PASETO specification
// defines. It is hoisted so recognising one allocates nothing per token.
var pasetoVersions = []string{"v1", "v2", "v3", "v4"}

// NewPasetoV4Issuer returns a PASETO v4.public issuer signing with Ed25519.
func NewPasetoV4Issuer(priv ed25519.PrivateKey, cfg PasetoIssuerConfig) (issuer coretoken.Issuer, err error) {
	binding, berr := bindEd25519Private(priv, coretoken.AlgorithmPasetoV4Public)
	//: the key length is checked before an issuer exists.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	issuing := cfg.issuing()
	//: a configuration that cannot be honoured is refused now (ADR 0031).
	if verr := validateIssuerConfig(issuing); verr != nil {
		//: propagate PolicyMisconfigured.
		return nil, verr
	}
	//: bound for life.
	return &pasetoIssuer{
		bound: binding, cfg: cfg, issuing: issuing,
		timeSource: resolveClock(cfg.Clock),
	}, nil
}

// NewPasetoV4Verifier returns a PASETO v4.public verifier bound to pub.
func NewPasetoV4Verifier(pub ed25519.PublicKey, cfg PasetoVerifierConfig) (verifier coretoken.Verifier, err error) {
	binding, berr := bindEd25519Public(pub, coretoken.AlgorithmPasetoV4Public)
	//: the key length is checked before a verifier exists.
	if berr != nil {
		//: propagate KeyUnsuitable.
		return nil, berr
	}
	policy, perr := newPolicy(cfg.verifying())
	//: every bound and knob is validated once, here.
	if perr != nil {
		//: propagate PolicyMisconfigured.
		return nil, perr
	}
	//: bound for life.
	return &pasetoVerifier{bound: binding, policy: policy, cfg: cfg}, nil
}

// checkPasetoScheme refuses every version+purpose except v4.public, telling a
// recognisable PASETO apart from a string that merely has dots in it.
func checkPasetoScheme(version, purpose string) error {
	//: the one scheme this package implements.
	if version == "v4" && purpose == "public" {
		//: accepted.
		return nil
	}
	//: a recognisable PASETO of another version or purpose — including
	//: v4.local — gets a verdict that says so, because "malformed" would send
	//: an operator looking for a corrupt token instead of a missing feature.
	if slices.Contains(pasetoVersions, version) && (purpose == "public" || purpose == "local") {
		//: name the scheme; it is public information, already on the wire.
		return errs.Wrap(SchemeUnsupported, errs.WrapParams{},
			errs.String("scheme", version+"."+purpose))
	}
	//: not a PASETO at all.
	return coretoken.Malformed
}
