// Package view — declares this package's own sentinels, plus the one helper
// through which every core/view port sentinel is raised.
//
// No Public string here names a template, a path, a line or a fragment of
// template source. html/template's diagnostics are unusually rich — a parse
// failure carries the file path and the offending function name, an escaping
// failure carries the escaper's internal state machine, and an execution
// failure carries the path, the line, the column, a fragment of the template's
// own source and a Go type name. Every one of those is useful to an operator
// and every one of them is reconnaissance to a stranger, so all of it travels
// as Fields and Private and none of it reaches a Public.
package view

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitConfig matches sysexits EX_CONFIG (78). A tree that will not parse is a
// permanent fault: the same files will be refused forever, and the fix is an
// edit to a template, never a retry.
const exitConfig int = 78

var (
	// TemplateSourceFailed is returned by the constructor when the template FS
	// could not be read.
	TemplateSourceFailed = errs.Define(CodeTemplateSourceFailed, "TEMPLATE_SOURCE_FAILED",
		"The view templates could not be read",
		"service/view: walking or reading Config.FS failed; the fields carry the path that failed and the underlying error, and the usual cause is an embed pattern that matched nothing",
		errs.WithExitCode(exitConfig))

	// TemplateParseFailed is returned by the constructor for a file
	// html/template refused to parse or could not build an escaping plan for.
	TemplateParseFailed = errs.Define(CodeTemplateParseFailed, "TEMPLATE_PARSE_FAILED",
		"A view template is not valid",
		"service/view: html/template refused a template at parse time or could not resolve its contextual escaping context; the fields carry the template name and the engine's own diagnostic",
		errs.WithExitCode(exitConfig))
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
