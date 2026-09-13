// Package entitlement — the three shapes every error here is built with, so the
// choice at a call site is which sentinel rather than which spelling.
//
// The split between the first two is whether this package DECIDED the failure
// or was TOLD about one. A decision has no cause to carry — nothing failed, a
// rule was applied to a filename or a mode — and the sentinel itself is the
// whole of it. A report from outside has a cause that must survive, because
// errors.Is(err, fs.ErrNotExist) and the text the operating system wrote are
// the two things a wrapper most often destroys.
//
// They are a copy of internal/service/entitlement's, and deliberately not an
// import: that package is a separate Go module, so its unexported helpers are
// unreachable from here, and exporting them would put three wrapping shapes on
// the surface of a domain whose whole public API is a three-method port and a
// facade. Thirty lines duplicated across a module boundary is the smaller cost,
// and the two copies are pinned to the same behaviour by having the same tests
// on both sides.
package entitlement

import (
	"errors"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// refuse reports a refusal this package reached on its own.
//
// The sentinel is passed as the CAUSE rather than as parameters, so errors.Is
// finds it by unwrapping as well as by (Code, Reason), and the fields carry
// every particular the public sentence deliberately leaves out — which here is
// a key directory, a subject and a file mode.
func refuse(sentinel *errs.Error, fields ...errs.FieldValue) error {
	//: origin wins — the sentinel keeps its code, reason and both messages,
	//: and this call adds only the fields.
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}

// classify reports a failure that arrived from OUTSIDE — the filesystem, or
// x/crypto/ssh's parsers — under the sentinel that says what it means here.
//
// The sentinel's Code, Reason, Public, Private and exit status are READ from it
// rather than restated at the call site: coreent.ErrNoPossession is reached
// from six sites in two files here, and four strings hand-copied six times with
// nothing comparing the copies is a drift generator.
//
// A nil sentinel is refused rather than dereferenced. Only a bug in this
// package can produce one, and the worst place to take a process down is the
// path that is already reporting a failure.
func classify(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	//: nothing to read the five values off.
	if sentinel == nil {
		//: Wrap answers a zero Code with INVALID_WRAP_PARAMS and keeps cause.
		return errs.Wrap(cause, errs.WrapParams{}, fields...)
	}
	code, _ := errs.CodeOf(sentinel)
	reason, _ := errs.ReasonOf(sentinel)
	//: ExitCode is read back rather than left at zero: WrapParams has no
	//: "inherit" spelling, and a zero there means the default 70 — which
	//: would silently drop EnrolmentFailed's EX_CANTCREAT on the
	//: stdlib-cause path while keeping it on the origin-wins one.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     code,
		Reason:   reason,
		Public:   errs.PublicOf(sentinel),
		Private:  errs.PrivateOf(sentinel),
		ExitCode: errs.ExitCodeOf(sentinel),
	}, fields...)
}

// foreignError hides a cause's SDK identity from errs.Wrap's origin-wins rule
// while leaving the cause matchable by errors.Is.
//
// It has deliberately NO Unwrap and no As. errs.Wrap reaches an inner
// *errs.Error with errors.AsType, which walks Unwrap and honours an As method,
// so providing either would defeat the whole point. What survives is
// errors.Is, because the stdlib consults an Is method before it unwraps.
//
// The cost is errors.As THROUGH this boundary: a caller cannot pull the
// consumer's own concrete error type back out of a roster refusal. That is
// accepted rather than overlooked — restoring it restores the hijack, and
// errors.Is plus the rendered text carry everything a caller acts on.
type foreignError struct {
	// cause is the error a caller's own code produced.
	cause error
}

// Error renders the consumer's message verbatim.
func (f foreignError) Error() string {
	//: The consumer's own account of what happened, unaltered.
	return f.cause.Error()
}

// Is delegates every sentinel question to the cause.
func (f foreignError) Is(target error) bool {
	//: Whatever the consumer's error matched before it crossed this boundary,
	//: it still matches.
	return errors.Is(f.cause, target)
}

// classifyForeign reports a failure produced by code the CALLER supplied, under
// the sentinel that says what it means here.
//
// It exists because origin-wins is the wrong rule at exactly these seams. Inside
// this SDK a deeper *errs.Error is the more specific classification and should
// win. A Getter, a BearerFetch, an ssh.Signer or a response Body is not deeper —
// it is somebody else's package, free to return an error from its own code
// range, and letting that win means errors.Is stops finding this domain's
// sentinel and a caller's exit-code table follows a number this SDK does not
// own.
//
// Measured, not reasoned: with a Getter returning an errs-typed error of its
// own, plain classify gives
// errors.Is(err, ErrRosterUnreachable)=false and code=0.3.48.1; this gives
// true and 0.2.35.7, with errors.Is(err, theConsumerError) still true in both.
//
// A cause with no SDK identity cannot hijack anything, so it takes the ordinary
// path untouched — which is why every stdlib cause behaves exactly as before.
func classifyForeign(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	inner, typed := errors.AsType[*errs.Error](cause)
	//: Nothing to neutralise: a plain error already leaves our params as
	//: origin, and a typed-nil takes Wrap's stdlib path for the same reason.
	if !typed || inner == nil {
		//: The ordinary path, byte for byte.
		return classify(sentinel, cause, fields...)
	}
	//: An SDK-typed error from outside this SDK. Hidden from origin-wins, kept
	//: matchable, and still rendered by foreignCause.
	return classify(sentinel, foreignError{cause: cause}, fields...)
}

// annotate attaches fields to an error without changing what it says.
//
// One call site needs it: ProvePossession naming which subject's private half
// it was loading when SignerFromFile refused. That refusal has already
// classified itself as absent, unreadable or unparseable, and replacing its
// identity with a fresh one would turn three operator situations into one.
//
// The guard is the same one service/entitlement's copy carries, for the same
// reason: errs.Wrap has no spelling for "add a field, decide nothing", and zero
// WrapParams over a cause with no *errs.Error returns CodeInvalidWrapParams —
// "internal wrap failure" — with the caller's error demoted to a cause. Every
// error reaching this particular call site is one of ours, so the guard never
// fires today; it is here because the next caller's might not be.
func annotate(err error, fields ...errs.FieldValue) error {
	//: nothing to annotate, and nothing to annotate it onto.
	if err == nil {
		//: the caller's nil travels back unchanged.
		return nil
	}
	inner, typed := errors.AsType[*errs.Error](err)
	//: no *errs.Error anywhere in the chain means no identity for origin-wins
	//: to preserve, and Wrap would substitute one of its own. A typed-nil is
	//: the same case: Wrap catches it and falls to the stdlib path, which with
	//: zero params is exactly the substitution this guard exists to avoid.
	if !typed || inner == nil {
		//: keep the caller's error exactly as it is; lose the field.
		return err
	}
	//: origin wins — code, reason and both messages stay the cause's.
	return errs.Wrap(err, errs.WrapParams{}, fields...)
}
