// Package token — the validated, defaults-applied form of a verification
// config, so the checks run once at construction rather than once per token.
package token

import (
	"time"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// policyValue is a VerifierConfig with every knob validated and every default
// resolved. It exists so a misconfiguration is a constructor error rather than
// a surprise on the first request (ADR 0031).
type policyValue struct {
	// issuer / audience / requireType are the configured expectations.
	issuer, audience, requireType string
	// leeway is the validated skew allowance.
	leeway time.Duration
	// maxLifetime is the validated exp-iat cap, zero when unchecked.
	maxLifetime time.Duration
	// maxTokenLen / maxClaimDepth / maxKeyCandidates are the resolved bounds.
	maxTokenLen, maxClaimDepth, maxKeyCandidates int
	// allowMissingExpiry mirrors the config knob.
	allowMissingExpiry bool
	// timeSource is the resolved clock, never nil.
	timeSource clock.Clock
}

// newPolicy validates cfg and resolves every default, or returns
// PolicyMisconfigured naming the offending knob.
func newPolicy(cfg VerifierConfig) (policy policyValue, err error) {
	//: skew tolerance must be non-negative and must stay tolerance.
	if cfg.Leeway < 0 || cfg.Leeway > MaxLeeway {
		//: name the knob, never the value's meaning.
		return policyValue{}, misconfigured("Leeway")
	}
	//: a negative cap is not a cap.
	if cfg.MaxLifetime < 0 {
		//: name the knob.
		return policyValue{}, misconfigured("MaxLifetime")
	}
	bounds, berr := resolveBounds(cfg)
	//: propagate the bound's own verdict unchanged.
	if berr != nil {
		//: refuse at construction.
		return policyValue{}, berr
	}
	//: every knob validated; assemble the immutable policy.
	return policyValue{
		issuer: cfg.Issuer, audience: cfg.Audience, requireType: cfg.RequireType,
		leeway: cfg.Leeway, maxLifetime: cfg.MaxLifetime,
		maxTokenLen: bounds.tokenLen, maxClaimDepth: bounds.claimDepth,
		maxKeyCandidates:   bounds.keyCandidates,
		allowMissingExpiry: cfg.AllowMissingExpiry,
		timeSource:         resolveClock(cfg.Clock),
	}, nil
}

// misconfigured returns the PolicyMisconfigured verdict naming one knob.
func misconfigured(knob string) error {
	//: origin-wins on the sentinel keeps its code, reason, public and exit.
	return errs.Wrap(coretoken.PolicyMisconfigured, errs.WrapParams{},
		errs.String("knob", knob))
}

// resolveClock substitutes the system clock for a nil one.
func resolveClock(configured clock.Clock) clock.Clock {
	//: nil is the documented "use the wall clock" spelling.
	if configured == nil {
		//: the production time source.
		return clock.System
	}
	//: the caller's clock — a ManualClock in tests.
	return configured
}
