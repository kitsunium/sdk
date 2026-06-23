// Package id — declares the sentinels returned by the Generator facade. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package id

import "github.com/kitsunium/sdk/internal/kernel/errs"

var (
	// UnknownScheme is returned when no Generator is registered under the
	// requested Scheme — typically a missing blank-import of the scheme.
	UnknownScheme = errs.Define(CodeUnknownScheme, "UNKNOWN_SCHEME",
		"No identifier generator is registered under that scheme",
		"core/id.New: scheme absent from registry; blank-import the scheme's package to register it")

	// DuplicateRegistration is the boot-time panic sentinel for the Generator
	// registry: a nil generator or a distinct generator claiming a taken Scheme.
	DuplicateRegistration = errs.Define(CodeDuplicateRegistration, "DUPLICATE_REGISTRATION",
		"A generator is already registered under that scheme",
		"core/id.Register: a distinct generator already claims this scheme, or a nil generator was supplied")
)
