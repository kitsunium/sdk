// Package validation — hosts ReportValue, the ordered collection of everything
// one validation found wrong.
package validation

import (
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxPathEchoRunes caps how much of the joined path list Err copies into its
// diagnostic field. Paths are field NAMES, never values, so echoing them is
// safe — but a 400-field form would otherwise produce a log line nobody reads.
const maxPathEchoRunes int = 240

// pathSeparator joins the paths Err echoes; the grammar itself never produces
// ", " inside a single path, so the join stays unambiguous.
const pathSeparator string = ", "

// truncationMark is appended to a clipped echo so a reader can tell a short
// list from a shortened one.
const truncationMark string = "…"

// ReportValue is every violation one validation found, in the order the
// constraints reported them. It is a SLICE rather than a struct so that the
// zero value — nil — is a passing report: a validator with no constraint
// passes, which is ADR 0046's first invariant and ADR 0031's "an empty
// configuration is not the same thing as a broken one".
//
// Composition is therefore plain append, and the accepting path allocates
// nothing at all.
//
// ReportValue is a published concrete shape (pkg/v1/validation.Report aliases
// it), so ADR 0040 applies: it may still change while the module is v0, said
// out loud, and not after v1.
type ReportValue []ViolationValue

// OK reports whether the value satisfied every constraint. It is the question
// to ask; comparing len(report) to zero says the same thing less clearly.
func (r ReportValue) OK() bool {
	//: nil and empty both mean "nothing was found wrong".
	return len(r) == 0
}

// First returns the earliest violation and whether there was one. Constraints
// report in the order they were composed, so "first" is the outermost rule on
// the earliest field — the one a stop-at-first caller wants.
func (r ReportValue) First() (violation ViolationValue, ok bool) {
	//: an OK report has no first violation to hand back.
	if len(r) == 0 {
		//: the zero ViolationValue with ok=false; callers must check ok.
		return ViolationValue{}, false
	}
	//: constraints append in composition order, so index 0 is the earliest.
	return r[0], true
}

// Paths returns the located path of every violation, in report order. It is
// the shape an HTTP layer wants when it maps a report onto a form: the caller
// already knows its own field names, and needs the SDK to say which ones.
func (r ReportValue) Paths() []string {
	//: an OK report yields nil rather than an empty slice — same convention as
	//: the report itself, and it keeps the accepting path allocation-free.
	if len(r) == 0 {
		//: nothing to list.
		return nil
	}
	//: exact capacity: one path per violation.
	paths := make([]string, 0, len(r))
	//: preserve report order; a shuffled list would make a diff of two runs
	//: unreadable, which is the whole point of collecting every violation.
	for _, violation := range r {
		//: the path is already in the JoinField/JoinIndex grammar.
		paths = append(paths, violation.Path)
	}
	//: hand back the ordered list.
	return paths
}

// Err converts the report to the SDK error model, so a validation result can
// satisfy an error-typed contract — principally core/config.Validator's
// Validate() error, which is how a decoded config struct self-checks.
//
// It returns a genuine nil interface when the report is OK. That is why
// ReportValue does not implement error itself: a value type implementing error
// and returned by value makes `if err != nil` true for a PASSING validation,
// the typed-nil trap errs.Wrap has an explicit case for.
//
// The error carries the violation COUNT and the list of paths, never the
// messages and never the values: the full report is the report. A caller that
// needs every message keeps the ReportValue; the error is the interop shape.
func (r ReportValue) Err() error {
	//: an OK report is not an error — return the nil interface, not a typed nil.
	if len(r) == 0 {
		//: nothing failed.
		return nil
	}
	//: the first violation identifies the report in one line of a log.
	first := r[0]
	//: fields carry the locations; Public stays the sentinel's fixed literal.
	return errs.Wrap(ValidationFailed, errs.WrapParams{},
		errs.Int("violations", len(r)),
		errs.String("rule", first.Rule),
		errs.String("paths", clipPaths(r)))
}

// clipPaths joins every violation path and shortens the result to
// maxPathEchoRunes runes, marking a truncation so a reader can tell a short
// list from a shortened one.
func clipPaths(report ReportValue) string {
	//: build the joined list first; paths are names, so this is safe to echo.
	joined := strings.Join(report.Paths(), pathSeparator)
	//: count RUNES, not bytes, so a non-ASCII field name is never cut in half.
	if len([]rune(joined)) <= maxPathEchoRunes {
		//: short enough to travel whole.
		return joined
	}
	//: clip on a rune boundary and mark it.
	return string([]rune(joined)[:maxPathEchoRunes]) + truncationMark
}
