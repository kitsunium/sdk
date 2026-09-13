// Package entitlement — the three shapes every error in this package is built
// with, so the choice at a call site is which sentinel rather than which
// spelling.
//
// The split between the first two is whether this package DECIDED the failure
// or was TOLD about one. A decision has no cause to carry — nothing failed, a
// rule was applied to a document — and the sentinel itself is the whole of it.
// A report from outside has a cause that must survive, because
// errors.Is(err, fs.ErrNotExist) and the text the operating system wrote are
// the two things a wrapper most often destroys.
//
// The third, annotate, adds a field to an error whose identity belongs to
// somebody else.
package entitlement

import (
	"errors"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fieldSeparator joins one rendered field to the next in a diagnostic line.
const fieldSeparator string = " "

// refuse reports a refusal this package reached on its own.
//
// The sentinel is passed as the CAUSE rather than as parameters, so errors.Is
// finds it by unwrapping as well as by (Code, Reason), and the fields carry
// every particular the public sentence deliberately leaves out.
func refuse(sentinel *errs.Error, fields ...errs.FieldValue) error {
	//: origin wins — the sentinel keeps its code, reason and both messages,
	//: and this call adds only the fields.
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}

// classify reports a failure that arrived from OUTSIDE — a transport, a
// filesystem, a decoder — under the sentinel that says what it means here.
//
// The sentinel's Code, Reason, Public, Private and exit status are READ from it
// rather than restated at the call site. A restatement is four strings copied
// by hand with nothing checking the copy, and coreent.ErrCIUnverifiable is
// reached from more than sixty sites in six files here: the first copy to drift
// would give one refusal two different sentences depending on which path
// produced it. misconfigured in product.go restates, and stays restating,
// because it is the only site that reaches CodeProductInvalid.
//
// When cause ALREADY carries an *errs.Error, errs.Wrap's origin-wins rule
// (ADR 0002 / SDK rule 6) keeps the cause's identity and the sentinel
// contributes only a trail entry. That is usually right — a refusal from deeper
// in this package has already classified itself — but it is NOT always what a
// call site wants: publishedJWKS borrows the roster fetch and must not let
// coreent.ErrRosterUnreachable out under a CI failure. Those sites pass the
// cause's TEXT as a field to refuse instead, and say so where they do it.
//
// A nil sentinel is refused rather than dereferenced. Only a bug in this
// package can produce one, and the worst place to take a process down is the
// path that is already reporting a failure. Neither sentinel.Code() nor
// errs.CodeOf(sentinel) is total here: a typed-nil *errs.Error is a non-nil
// error interface, so the accessor finds it in the chain and dereferences it
// exactly as the method would.
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
	//: would silently drop a sibling sentinel's exit status on the
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
// Two call sites need it and both are the same situation: a refusal that has
// already classified itself, plus one fact the layer above knows — which origin
// produced it, or that the CI seat was refused too.
//
// The guard is load-bearing rather than defensive. errs.Wrap has no spelling
// for "add a field, decide nothing": zero WrapParams over a cause that carries
// no *errs.Error fails validateDefineArgs and returns CodeInvalidWrapParams —
// "internal wrap failure" — with the caller's error demoted to a cause. That is
// reachable in production, not only in theory: coreent.Identity is a PORT, and
// a consumer implementing it with plain errors.New would have every device
// refusal replaced by a meta-code on the one path that reports it. So an error
// with no identity of its own is returned untouched, and the annotation is the
// thing that is dropped.
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

// diagnose renders the half of an error that is NOT wire-safe: the private
// sentence, every field, and the foreign cause underneath.
//
// It exists because the public sentence is now all err.Error() carries, and one
// place in this package writes to a LOG rather than to a wire — rememberRoster,
// reporting a cache it could not replace. An operator reading that line is owed
// the directory and what the filesystem said about it, and a terminal they own
// is not a response body.
//
// An error with nothing to add renders as the empty string, which is what a
// caller interpolating it wants: nothing printed. That is a rendering rather
// than a sentinel, so there is no second result to pair it with.
func diagnose(err error) string {
	var parts []string
	//: the private sentence, when this error has one.
	if private := errs.PrivateOf(err); private != "" {
		parts = append(parts, private)
	}
	//: then everything the public sentence deliberately left out.
	if detail := particulars(err); detail != "" {
		parts = append(parts, detail)
	}
	//: one rendered line, private sentence first.
	return strings.Join(parts, fieldSeparator)
}

// particulars renders the FIELDS and the foreign cause of an error, and
// deliberately not its Private sentence.
//
// publishedJWKS is why it is separate from diagnose. It borrows the roster
// fetch, so the error it holds carries coreent.ErrRosterUnreachable's private
// half — "no origin answered and nothing was cached" — which is the roster
// vocabulary that whole function exists to keep out of a CI failure. What it
// wants is the url the fetch tried and the word the transport used, which is
// exactly what the fields and the cause hold and nothing else.
//
// Reading the cause's fields rather than its Error() is not a refinement: after
// this package's conversion, Error() is the wire-safe sentence and carries no
// url and no syscall text at all, so a call site that kept spelling it
// cause.Error() would have silently swapped a diagnosis for a tautology.
func particulars(err error) string {
	var parts []string
	//: every particular the public sentence deliberately left out.
	for _, field := range errs.FieldsOf(err) {
		parts = append(parts, field.Key()+"="+field.StringValue())
	}
	//: and what the world outside this package actually said, which is the
	//: half an operator acts on and the half no field can restate.
	if cause := foreignCause(err); cause != "" {
		parts = append(parts, "cause="+cause)
	}
	//: one rendered line, in the order the error was built.
	return strings.Join(parts, fieldSeparator)
}

// foreignCause returns the message of the first error in the chain that this
// SDK did not build, and the empty string when the chain is ours all the way
// down.
//
// Quoting our own Public back at the reader would be worse than saying nothing:
// it is the sentence printed one line above, and it is precisely NOT the
// filesystem's or the transport's account of what happened.
func foreignCause(err error) string {
	var cause string
	//: walk down until something that is not an *errs.Error answers.
	for current := err; current != nil; current = errors.Unwrap(current) {
		//: one of ours — its own Public is already on the line above.
		if _, ours := current.(*errs.Error); ours {
			continue
		}
		//: the first foreign link is the one that has news.
		cause = current.Error()

		break
	}
	//: what the world outside this SDK said, if anything did.
	return cause
}
