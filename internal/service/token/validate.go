// Package token — the claim validation both formats share.
//
// Everything here runs AFTER the signature has verified, and that ordering is a
// security property, not an implementation detail: reporting "expired" for a
// token whose signature was never checked tells an attacker what is inside a
// forgery, and hands the application a claim set it may log.
package token

import (
	"slices"
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// validateIssuerConfig refuses an IssuerConfig that cannot be honoured, before
// it becomes an issuer whose every call fails (ADR 0031).
func validateIssuerConfig(cfg IssuerConfig) error {
	//: a negative lifetime would mint a token that expired before it existed.
	if cfg.Lifetime < 0 {
		//: name the knob so the fix is greppable.
		return errs.Wrap(coretoken.PolicyMisconfigured, errs.WrapParams{},
			errs.String("knob", "Lifetime"))
	}
	//: a zero Lifetime is legal — the caller may stamp exp on each claim set,
	//: and stampIssuedClaims refuses at Issue time if they do not.
	return nil
}

// validateClaims applies the policy's temporal and identity checks to an
// already-authenticated claim set, returning the first verdict that refuses it.
//
// The order is cheapest-and-most-common first: a token is far more often
// expired than addressed to the wrong service.
func (p policyValue) validateClaims(claims coretoken.ClaimsValue) error {
	now := p.timeSource.Now()
	//: expiry, including the "there wasn't one" case.
	if err := p.checkExpiry(claims, now); err != nil {
		//: first refusal wins.
		return err
	}
	//: not-before, when the token carries one.
	if nbf := claims.NotBefore(); !nbf.IsZero() && now.Before(nbf.Add(-p.leeway)) {
		//: the token exists but is not live yet.
		return coretoken.NotYetValid
	}
	//: lifetime cap, when the recipient set one.
	if err := p.checkLifetime(claims); err != nil {
		//: refuse a token whose validity window is longer than we accept.
		return err
	}
	//: who minted it and who it is for.
	return p.checkIdentity(claims)
}

// checkIdentity enforces the issuer and audience expectations, each only when
// the caller named one — this package has no opinion about who should have
// minted a token, only about whether it matches what the caller said.
func (p policyValue) checkIdentity(claims coretoken.ClaimsValue) error {
	//: issuer (RFC 8725 §3.8).
	if p.issuer != "" && claims.Issuer() != p.issuer {
		//: a token from another issuer is not this recipient's token.
		return coretoken.IssuerMismatch
	}
	//: audience (RFC 8725 §3.9).
	if p.audience != "" && !slices.Contains(claims.Audience(), p.audience) {
		//: correctly signed, addressed elsewhere: still invalid here.
		return coretoken.AudienceMismatch
	}
	//: both configured checks passed.
	return nil
}

// checkExpiry enforces the exp claim and the profile's requirement that one be
// present.
//
// RFC 7519 §4.1.4 makes "exp" OPTIONAL, which means a conforming token can be
// valid forever. A bearer token that never expires is a password with no
// rotation story, so the default here inverts the RFC's optionality — and the
// opt-out is a named field rather than a silent absence.
func (p policyValue) checkExpiry(claims coretoken.ClaimsValue, now time.Time) error {
	exp := claims.Expiry()
	//: absence is a policy question, not a comparison.
	if exp.IsZero() {
		//: the profile decides; the default is to require one.
		if p.allowMissingExpiry {
			//: the caller opted out by name.
			return nil
		}
		//: refuse the eternal token.
		return coretoken.ExpiryRequired
	}
	//: expired once now is strictly past exp plus the skew allowance.
	if now.After(exp.Add(p.leeway)) {
		//: the token was valid; it no longer is.
		return coretoken.Expired
	}
	//: still inside its window.
	return nil
}

// checkLifetime enforces MaxLifetime over the exp-iat span.
//
// It requires "iat". A token with an expiry but no issue time has a validity
// window this recipient cannot measure, and "I could not measure it" is not a
// reason to accept it — the whole point of the cap is to refuse a credential
// that outlives the recipient's tolerance for one it cannot revoke.
func (p policyValue) checkLifetime(claims coretoken.ClaimsValue) error {
	//: no cap configured means no check.
	if p.maxLifetime <= 0 {
		//: nothing to enforce.
		return nil
	}
	exp, iat := claims.Expiry(), claims.IssuedAt()
	//: an unmeasurable lifetime is not an accepted one.
	if exp.IsZero() || iat.IsZero() {
		//: say which half was missing, without quoting either claim.
		return errs.Wrap(coretoken.LifetimeTooLong, errs.WrapParams{},
			errs.String("reason", "exp or iat absent"))
	}
	//: the span the issuer minted.
	if exp.Sub(iat) > p.maxLifetime {
		//: longer than this recipient accepts.
		return coretoken.LifetimeTooLong
	}
	//: inside the cap.
	return nil
}

// stampIssuedClaims applies an issuer's configured defaults to claims on the
// way out: "iss" when the claims carry none, "iat" always, and "exp" derived
// from the configured lifetime when the claims carry no expiry of their own.
//
// It refuses to mint a token with no expiry unless the issuer was built with
// AllowMissingExpiry, which is the same rule the verifier applies, stated once
// on each side so neither can drift into being the lenient one.
func stampIssuedClaims(claims coretoken.ClaimsValue, cfg IssuerConfig, source clock.Clock) (stamped coretoken.ClaimsValue, err error) {
	now := source.Now()
	//: the caller's explicit issuer wins; the config fills a gap.
	if claims.Issuer() == "" && cfg.Issuer != "" {
		//: stamp the configured issuer.
		claims = claims.WithIssuer(cfg.Issuer)
	}
	//: iat is always stamped unless the caller pinned one.
	if claims.IssuedAt().IsZero() {
		//: second precision — every JWT reader agrees on integers.
		claims = claims.WithIssuedAt(now.Truncate(time.Second))
	}
	//: an explicit expiry from the caller is never overwritten.
	if !claims.Expiry().IsZero() {
		//: nothing left to derive.
		return claims, nil
	}
	//: derive one from the configured lifetime when there is one.
	if cfg.Lifetime > 0 {
		//: exp = now + Lifetime, at second precision.
		return claims.WithExpiry(now.Add(cfg.Lifetime).Truncate(time.Second)), nil
	}
	//: no expiry anywhere: the issuer must have asked for that explicitly.
	if cfg.AllowMissingExpiry {
		//: mint the eternal token the caller asked for by name.
		return claims, nil
	}
	//: refuse — the same verdict the verifier would return for the result.
	return coretoken.ClaimsValue{}, coretoken.ExpiryRequired
}
