// Package config — range 0.2.10.* (ADR 0028 core/app/config block).
//
// Package config — declares the sentinel *errs.Error values. Each var's name
// equals its errs.Define Reason in SCREAMING_SNAKE form.
package config

// exitConfig matches sysexits EX_CONFIG (78) — a configuration fault is neither
// a generic internal error nor transient unavailability.
const exitConfig int = 78
