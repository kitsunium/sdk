// Package view — the one helper through which every core/app/view sentinel is
// raised. The domain's codes and sentinels — the port's, and this engine's
// construction failures — are declared in internal/core/app/view (ADR 0160).
//
// No Public string the engine raises names a template, a path, a line or a
// fragment of template source. html/template's diagnostics are unusually rich
// — a parse failure carries the file path and the offending function name, an
// escaping failure carries the escaper's internal state machine, and an
// execution failure carries the path, the line, the column, a fragment of the
// template's own source and a Go type name. Every one of those is useful to an
// operator and every one of them is reconnaissance to a stranger, so all of it
// travels as Fields and Private and none of it reaches a Public.
package view

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// raise wraps a port or package sentinel, attaching fields without relabelling
// it — errs.Wrap is origin-wins (rule 6), so the sentinel's Code, Reason,
// Public and Private survive and only the Fields grow.
//
// It is the single place the engine's own diagnostic is attached, and it
// attaches it as a FIELD because that is the only dynamic channel the error
// model offers: Public is a compile-time literal by design, and origin-wins
// forbids a wrapper from supplying a new Private. Fields are log-side — they
// are absent from err.Error() (ADR 0005 §Semantics) — so a framework that
// serialises FieldsOf into a response has re-opened the split this domain
// depends on.
func raise(sentinel *errs.Error, cause error, fields ...errs.FieldValue) error {
	//: a nil cause carries no engine diagnostic — the sentinel says everything.
	if cause == nil {
		//: fields only; origin-wins keeps the sentinel's Code/Reason/Public.
		return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
	}
	//: the engine's message is the leak-prone half; it lands in a field and
	//: never in the Public the caller may forward to a browser.
	return errs.Wrap(sentinel, errs.WrapParams{},
		append(fields, errs.String("engine_error", cause.Error()))...)
}
