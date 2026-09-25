// Package secret — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public and no Private here carries a secret, and no field ever will: the
// whole domain exists so that a value can travel through a program without
// being written down by accident, and an error message is the most-written
// text a program produces. A secret's NAME is not secret and may travel as a
// log-only field, because an operator cannot act on "a secret is missing"
// without knowing which one.
package secret

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A missing secret, a malformed
// name and a write to a read-only store are all wiring faults: the same call
// is refused identically forever and the fix is in the deployment or at the
// call site, never a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A backend that could not be
// reached may answer the next attempt, which is exactly what distinguishes
// StoreUnavailable from every other verdict in this block.
const exitTempFail int = 75

// httpUnavailable is 503: the store, not the request, is the problem.
const httpUnavailable int = 503

var (
	// NotFound is returned by Get, Versions and Prune for a name that holds
	// no version — never written to a writable store, or supplied by nobody
	// to a read-only one.
	NotFound = errs.Define(CodeNotFound, "NOT_FOUND",
		"No secret has that name",
		"core/secret: the store holds no version under the requested name; the field names it, the value never existed",
		errs.WithExitCode(exitConfig))

	// InvalidName is returned by every Store method, and by ValidateName, for
	// a name outside the grammar. The grammar is closed on purpose: a name
	// becomes a file name in one store and a variable name in another, and a
	// character one of them treats specially would make two stores disagree
	// about which secret a name designates.
	InvalidName = errs.Define(CodeInvalidName, "INVALID_NAME",
		"That is not a valid secret name",
		"core/secret: a name must be 1-63 characters of a-z, 0-9 and '-', starting and ending with a letter or a digit",
		errs.WithExitCode(exitConfig))

	// ReadOnly is returned by Put and Prune on a store that only reads. The
	// environment is the shipped example: the process did not write its own
	// environment and cannot rotate what an orchestrator mounted.
	ReadOnly = errs.Define(CodeReadOnly, "READ_ONLY",
		"This secret store cannot be written",
		"core/secret: Put or Prune was called on a store that only reads — rotate at the source that supplies it",
		errs.WithExitCode(exitConfig))

	// StoreUnavailable is returned when the backend itself failed. It carries
	// EX_TEMPFAIL and 503 because a retry is meaningful, which is what
	// separates it from every other verdict in this block.
	StoreUnavailable = errs.Define(CodeStoreUnavailable, "STORE_UNAVAILABLE",
		"The secret store is unavailable",
		"core/secret: the backend could not be read or written, or its lock could not be taken; the fields name the operation and the secret, never a value",
		errs.WithExitCode(exitTempFail), errs.WithHTTPStatus(httpUnavailable))

	// ValueRefused is returned by Value.UnmarshalJSON and Value.UnmarshalText.
	//
	// Two inputs are refused. A JSON token that is not a string: a number has
	// already been re-spelled on its way to the decoder — 1e3 arrives as 1000
	// and a twenty-digit token loses its tail to float64 — so accepting it
	// would store a secret the operator never wrote. And the redaction
	// placeholder itself: it is what every rendering of a Value writes, so
	// finding it on the way IN means a rendered configuration was fed back as
	// a real one, and every secret in it would silently become the placeholder.
	ValueRefused = errs.Define(CodeValueRefused, "VALUE_REFUSED",
		"A secret can only be decoded from a string, and never from its own placeholder",
		"core/secret: the decoder was given a non-string JSON token or the redaction placeholder; quote the secret, and never reload a rendered configuration",
		errs.WithExitCode(exitConfig))

	// EmptyValue is returned by Put for an empty secret. Storing one would
	// turn an unfilled field into a version every reader then trusts (ADR
	// 0031: a zero value is refused where no default is safe, and no secret
	// is a safe default).
	EmptyValue = errs.Define(CodeEmptyValue, "EMPTY_VALUE",
		"An empty secret cannot be stored",
		"core/secret: Put was given a zero Value; an empty secret is what an unfilled field looks like",
		errs.WithExitCode(exitConfig))

	// InvalidKeep is returned by Prune when asked to keep fewer than one
	// version. Keeping none would delete the secret, and deletion is a
	// different decision from pruning — one a rotation must never take by
	// arithmetic accident.
	InvalidKeep = errs.Define(CodeInvalidKeep, "INVALID_KEEP",
		"A prune must keep at least one version",
		"core/secret: Prune was asked to keep fewer than one version, which would delete the secret rather than prune it",
		errs.WithExitCode(exitConfig))
)
