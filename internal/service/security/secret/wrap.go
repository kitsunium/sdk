// Package secret — the two ways this package turns a failure into a verdict:
// storeFailure, around the error a caller's SubjectKeyStore returned, and
// wrapAs, around one of the domain's sentinels. Every sentinel and code it
// raises is declared in internal/core/security/secret (ADR 0160); this
// package declares none.
//
// No Public, Private or field built here carries a secret, a data key or a
// sealed box. A secret's name, a version number, an environment variable's
// NAME, a valid subject reference and an operation may travel as log-only
// fields; a file path does not, because the directory a store owns is a
// deployment detail an error has no reason to repeat.
package secret

import (
	coresecret "github.com/kitsunium/sdk/internal/core/security/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitTempFail matches sysexits EX_TEMPFAIL (75), the exit status of core
// StoreUnavailable: a subject-key store that failed may answer the next
// attempt, and a verdict built around a plain store error carries it too.
const exitTempFail int = 75

// storeFailure is the verdict for an error a SubjectKeyStore method returned:
// the retryable core StoreUnavailable, naming the operation and — when the
// call was about one — the subject.
//
// It wraps the store's error rather than the sentinel, as the state-machine
// engine does with its own caller's store: a store error that is itself an SDK
// error keeps its code — origin wins — and gains StoreUnavailable in its trail,
// and a plain error stays reachable through errors.Is. A caller's store is the
// caller's code, so its message is the caller's to keep free of secrets; the
// engine adds only a valid subject, which is a reference and never a value.
func storeFailure(cause error, operation, subject string) error {
	fields := []errs.FieldValue{errs.String("operation", operation)}
	//: a call about the whole store names no subject.
	if subject != "" {
		fields = append(fields, errs.String("subject", subject))
	}
	//: origin wins when the store's error is already an SDK error.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coresecret.CodeStoreUnavailable,
		Reason:   "STORE_UNAVAILABLE",
		Public:   coresecret.StoreUnavailable.Public(),
		Private:  coresecret.StoreUnavailable.Private(),
		ExitCode: exitTempFail,
	}, fields...)
}

// wrapAs returns the given sentinel as the error origin — its code, reason and
// public message win — with the cause's message and any extra fields attached
// as log-only metadata.
//
// Wrapping the sentinel rather than the cause is what keeps this domain's
// verdict intact: errs.Wrap is origin-wins, so an *errs.Error cause passed
// first would hijack the code. Callers pass a cause only when its message is
// known not to carry a secret — an I/O error, a lock error — never a decoder's
// message.
func wrapAs(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	//: a nil cause contributes no field.
	if cause == nil {
		//: the sentinel with the caller's fields only.
		return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
	}
	//: the cause travels as a log-only field.
	return errs.Wrap(sentinel, errs.WrapParams{}, append(fields, errs.String("cause", cause.Error()))...)
}
