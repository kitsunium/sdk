package jsonschema

import (
	"github.com/kitsunium/sdk/internal/core/app/validation"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxEvalDepth bounds the schemas an evaluation may enter at once: a "$ref"
// cycle that consumes no part of the document would otherwise never end.
const maxEvalDepth int = 512

// check is one compiled keyword: it reports whether v satisfies it at path,
// recording the violations when the evaluation collects them.
type check func(e *eval, v *value, path string) bool

// schema is a compiled schema: a boolean one, or the checks of its keywords.
type schema struct {
	// always is a boolean schema's verdict; nil for an object schema.
	always *bool
	// checks are the keywords that assert, in document order.
	checks []check
}

// eval is one evaluation: the violations it collects — none when it only
// answers yes or no, as inside anyOf, oneOf, not and if — and how deep it is.
type eval struct {
	// report holds the violations collected.
	report validation.ReportValue
	// collect is whether violations are recorded.
	collect bool
	// depth is how many schemas are being evaluated at once.
	depth int
	// looped is set when depth passed maxEvalDepth.
	looped bool
}

// quiet returns an evaluation of the same depth that collects nothing: a
// combinator asks it yes or no and reports once itself.
func (e *eval) quiet() *eval {
	//: no report, the same depth.
	return &eval{depth: e.depth}
}

// absorb carries a quiet evaluation's loop detection back.
func (e *eval) absorb(q *eval) {
	//: a loop anywhere ends the evaluation.
	e.looped = e.looped || q.looped
}

// fail records a violation when collecting, and reports false.
func (e *eval) fail(path, rule, message string, code errs.Code) bool {
	//: recorded only when the caller wants the report.
	if e.collect {
		e.report = append(e.report, validation.ViolationValue{Path: path, Rule: rule, Message: message, Code: code})
	}
	return false
}

// validate evaluates v at path against s: every check runs when collecting,
// so the report is complete; a quiet evaluation stops at the first failure.
func (s *schema) validate(e *eval, v *value, path string) bool {
	//: a boolean schema.
	if s.always != nil {
		//: false allows nothing.
		if !*s.always {
			return e.fail(path, "false", "no value is allowed here", violationCode)
		}
		return true
	}
	//: a cycle that consumes nothing ends here.
	if e.depth >= maxEvalDepth {
		e.looped = true
		return false
	}
	e.depth++
	defer func() { e.depth-- }()
	ok := true
	for _, c := range s.checks {
		if !c(e, v, path) {
			ok = false
			if !e.collect || e.looped {
				return false
			}
		}
	}
	return ok
}
