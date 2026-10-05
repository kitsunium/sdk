// Package validation — ranges 0.2.15.* (ADR 0046 core/app/validation block)
// and 0.3.47.* (ADR 0046 service/app/validation block, declared here since
// ADR 0160).
//
// Package validation — declares the sentinel *errs.Error outcomes of the
// domain: the port's, and those the struct-tag compiler emits. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// The VIOLATION codes (0.3.47.1 - 0.3.47.5) deliberately have no sentinel: a
// violation is a ViolationValue, not an error, and minting five unused
// *errs.Error values to mirror them would invite exactly the confusion ADR 0046
// §Violations are not errors exists to prevent.
//
// Package validation — defines the path grammar every ViolationValue speaks.
//
// Package validation — hosts ReportValue, the ordered collection of everything
// one validation found wrong.
//
// Package validation declares the SDK's value-checking port: the [Constraint]
// that inspects one value, the [ViolationValue] it reports when that value is
// not acceptable, and the [ReportValue] that collects them. A core sibling
// admitted by ADR 0046.
//
// The port is a FUNCTION type rather than an interface — the shape
// internal/core/CLAUDE.md already admits for resilience.Operation and
// scheduler.Job/Schedule. ADR 0039's rule is that a published port must not
// grow a method, because pkg/v1 aliases publish the shape and Go interfaces are
// structural; a func type satisfies that rule structurally, since it cannot
// grow a method at all.
//
// # What this package is not
//
// It is not internal/core/app/config.Validator. That port is a decoded config
// struct's SELF-check — `Validate() error`, one boolean answer, called once by
// config.Load. This package is the engine that answers it: a struct implements
// config.Validator by running its constraints and returning ReportValue.Err().
// The two compose; neither replaces the other.
//
// The concrete constraints (presence, bounds, length, set membership, pattern),
// the combinators that descend into fields and elements, and the struct-tag
// front end live in internal/service/app/validation. This package owns the
// contract, the two domain values, the path grammar, and the typed sentinels.
//
// Package validation — hosts ViolationValue, the located statement that one
// value failed one rule.
package validation
