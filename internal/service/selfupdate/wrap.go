// Package selfupdate — the two shapes every error in this package is built
// with, so the choice at a call site is which sentinel rather than which
// spelling.
//
// The split between them is whether this package DECIDED the failure or was
// TOLD about one. A decision has no cause to carry — nothing failed, a rule
// was applied — and the sentinel itself is the whole of it. A report from
// outside has a cause that must survive, because `errors.Is(err, os.ErrPermission)`
// and the text the operating system wrote are the two things a wrapper most
// often destroys.
package selfupdate

import "github.com/kitsunium/sdk/internal/kernel/errs"

// refuse reports a refusal this package reached on its own.
//
// The sentinel is passed as the CAUSE rather than as parameters, so
// errors.Is finds it by unwrapping as well as by (Code, Reason), and the
// fields carry every particular the public sentence deliberately leaves out.
func refuse(sentinel *errs.Error, fields ...errs.FieldValue) error {
	//: origin wins — the sentinel keeps its code, reason and both messages,
	//: and this call adds only the fields.
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}

// classify reports a failure that arrived from OUTSIDE — a transport, a
// filesystem, a decoder — under the sentinel that says what it means here.
//
// The sentinel's Code, Reason, Public, Private and exit status are READ from
// it rather than restated at the call site. A restatement is four strings
// copied by hand with nothing checking the copy, and this package reaches
// DownloadFailed from twelve sites in five files, eight of them through here:
// the first copy to drift would give one failure two different sentences
// depending on which call path produced it.
// TestClassifyCarriesTheSentinelVerbatim is what makes that impossible rather
// than unlikely.
//
// When cause ALREADY carries an *errs.Error, errs.Wrap's origin-wins rule
// (ADR 0002 / SDK rule 6) keeps the cause's identity and the sentinel
// contributes only a trail entry. That is the right outcome, and it is why
// every call here can name its most specific classification without first
// checking whether the cause has one of its own.
//
// A nil sentinel is refused rather than dereferenced. Only a bug in this
// package can produce one, and the worst place to take a process down is the
// path that is already reporting a failure — so it degrades to what
// refuse(nil) would do, a typed INVALID_WRAP_PARAMS still carrying the cause.
// Neither sentinel.Code() nor errs.CodeOf(sentinel) is total here: a typed-nil
// *errs.Error is a non-nil error interface, so the accessor finds it in the
// chain and dereferences it exactly as the method would.
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
	//: would silently drop EX_CANTCREAT on the stdlib-cause path while
	//: keeping it on the origin-wins one.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     code,
		Reason:   reason,
		Public:   errs.PublicOf(sentinel),
		Private:  errs.PrivateOf(sentinel),
		ExitCode: errs.ExitCodeOf(sentinel),
	}, fields...)
}
