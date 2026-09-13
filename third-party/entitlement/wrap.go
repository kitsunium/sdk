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
