//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/validation .

// Package validation is the public facade for the SDK's value-checking
// domain: a small set of constraints, the combinators that compose them, and a
// struct-tag front end — all producing one [Report] that says WHERE each
// failure is and WHY.
//
//	type Address struct {
//	    Zip string `json:"zip" validate:"required,minlen=4,maxlen=10"`
//	}
//	type User struct {
//	    Name      string    `json:"name"      validate:"required,maxlen=64"`
//	    Age       int       `json:"age"       validate:"min=0,max=130"`
//	    Addresses []Address `json:"addresses" validate:"mincount=1,dive"`
//	}
//
//	rules, err := validation.Struct[User](validation.StructConfig{})
//	if err != nil {
//	    return err // a tag that cannot be honoured is refused HERE, not at the first request
//	}
//	report := rules(validation.RootPath, user)
//	for _, v := range report {
//	    log.Warn("invalid", "path", v.Path, "rule", v.Rule, "why", v.Message)
//	}
//	// path == "addresses[2].zip"
//
// # A violation says where
//
// Every [Violation] carries a Violation.Path in one grammar: members joined
// by ".", elements by "[n]" — "user.addresses[2].zip". [RootPath] (the empty
// string) is the value as a whole, which is what a cross-field rule reports.
// Build child paths with [JoinField] and [JoinIndex]; never concatenate by
// hand, or the grammar stops being one.
//
// Member names come from the json tag when the field has one, else the Go
// field name. That is not a preference: the SDK's own config.Load decodes
// every format — TOML, YAML, env, JSON — through a JSON round trip, so the
// json name is literally the key the operator wrote. For the same reason an
// embedded struct with no json name adds no segment of its own: encoding/json
// promotes its fields into the enclosing object, so an untagged Common
// embedding reports "zip", not "Common.zip". And a rule on a field
// encoding/json never decodes a key into — hidden by a shallower field of the
// same name, or tied with another at its depth, which JSON resolves to
// neither — is refused when the type compiles, since no input could ever set
// the value it judges.
//
// # Everything, not the first thing
//
// The default collects EVERY violation, in a stable order. A form that reports
// one error at a time makes the user submit it five times. [First] opts into
// stopping, and StructConfig.StopAtFirst does the same for a tag validator —
// both really stop, rather than filtering a full report afterwards.
//
// # A violation is not an error, and a report is not an error either
//
// A [Violation] is a value: a validation normally produces several, and
// errors.Join of five would render five bracket headers and bury the paths.
// A [Report] is a slice of them — its zero value, nil, is a PASSING report.
// It does not implement error, because a value type that implements error and
// is returned by value makes `if err != nil` true for a passing validation.
// Report.Err converts on demand and returns a genuine nil interface when
// there is nothing wrong; that error carries the count and the paths, never
// the messages and never the values. The full report is the report.
//
// # A message never contains the value
//
// This is a security property, not a style rule. A validation message is the
// one error message in a service designed to reach the end user, so a message
// that echoed the value would exfiltrate whatever was validated — a password,
// a token, a card number. Every built-in message names the rule and the bound.
//
// # Feeding config.Validator
//
// This package is the engine; config.Validator is the contract a decoded
// config struct implements to self-check after config.Load. They compose:
//
//	func (c Conf) Validate() error {
//	    rules, err := validation.Struct[Conf](validation.StructConfig{})
//	    if err != nil {
//	        return err
//	    }
//	    return validation.Check(c, rules)
//	}
//
// # No constraint is legitimate; a broken constraint is not
//
// A validator with no rules passes, and a type with no validate tag compiles
// to an empty plan that accepts everything — adding validation to a type has
// to be possible one field at a time. What is refused is a constraint that
// could never be honoured: an inverted interval ([Between]), an empty allowed
// set ([OneOf]), an uncompilable pattern ([Matches]), a tag rule the field's
// kind cannot answer. All of them are refused at CONSTRUCTION (ADR 0031).
package validation

import (
	"cmp"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"
)

const (
	// RootPath is the path of the value handed to a validator, before any
	// descent. A violation reported there is about the value as a whole.
	RootPath string = corevalidation.RootPath
	// Unbounded is the [Length] / [Count] upper bound that means "no ceiling".
	Unbounded int = svcvalidation.Unbounded
	// CodeRequired identifies an absent value.
	CodeRequired errs.Code = svcvalidation.CodeRequired
	// CodeOutOfRange identifies an ordered value outside its bounds.
	CodeOutOfRange errs.Code = svcvalidation.CodeOutOfRange
	// CodeLengthOutOfRange identifies a size outside its bounds.
	CodeLengthOutOfRange errs.Code = svcvalidation.CodeLengthOutOfRange
	// CodeNotInSet identifies a value outside its allowed set.
	CodeNotInSet errs.Code = svcvalidation.CodeNotInSet
	// CodePatternMismatch identifies a string that did not match its pattern.
	CodePatternMismatch errs.Code = svcvalidation.CodePatternMismatch
)

var (
	// Failed is the error a non-empty Report converts to. Its HTTP status is
	// 422: the request was syntactically fine and semantically wrong, which is
	// what a failed validation is — not a 500.
	Failed = corevalidation.ValidationFailed
	// Misconfigured is returned by a constraint CONSTRUCTOR whose arguments it
	// cannot honour — an inverted interval, an empty set, a bad pattern.
	Misconfigured = corevalidation.ConstraintMisconfigured
	// InvalidRule is returned by Struct when a validate tag cannot be compiled.
	InvalidRule = svcvalidation.InvalidRule
	// UnsupportedTarget is returned by Struct when T is not a struct type.
	UnsupportedTarget = svcvalidation.UnsupportedTarget
)

// Constraint is the public alias for the value-checking port. It is a FUNC
// type, so it cannot grow a method and break a downstream implementer
// (ADR 0039).
type Constraint[T any] = corevalidation.Constraint[T]

// Violation is the public alias for one located failure.
type Violation = corevalidation.ViolationValue

// Report is the public alias for everything one validation found wrong. Its
// zero value — nil — is a passing report.
type Report = corevalidation.ReportValue

// StructConfig is the public alias for a compiled struct validator's options.
type StructConfig = svcvalidation.StructConfig

// JoinField extends a path with a member name — JoinField("user", "zip") is
// "user.zip". The grammar does not quote, so a member name that itself
// contains '.', '[' or ']' yields a path indistinguishable from a nested or
// indexed one.
func JoinField(base, name string) string {
	//: delegate to the core grammar.
	return corevalidation.JoinField(base, name)
}

// JoinIndex extends a path with an element position — JoinIndex("a", 2) is
// "a[2]".
func JoinIndex(base string, index int) string {
	//: delegate to the core grammar.
	return corevalidation.JoinIndex(base, index)
}

// All runs every constraint and reports everything they found. It is the
// default shape of the engine; All with no constraint accepts everything.
func All[T any](constraints ...Constraint[T]) Constraint[T] {
	//: delegate to the service combinator.
	return svcvalidation.All(constraints...)
}

// First runs the constraints in order and stops at the one that refuses. It is
// a real short-circuit, not a filtered report.
func First[T any](constraints ...Constraint[T]) Constraint[T] {
	//: delegate to the service combinator.
	return svcvalidation.First(constraints...)
}

// Check runs constraints against value at the root path and converts the
// result to the SDK error model — the one-line bridge to config.Validator.
func Check[T any](value T, constraints ...Constraint[T]) error {
	//: delegate to the service helper.
	return svcvalidation.Check(value, constraints...)
}

// Must returns constraint, panicking when err is non-nil. It is the
// regexp.MustCompile idiom, for a package-level validator built from literals
// at init. Do NOT use it on a bound that comes from configuration.
func Must[T any](constraint Constraint[T], err error) Constraint[T] {
	//: delegate to the service helper.
	return svcvalidation.Must(constraint, err)
}

// Field applies constraints to a member of T reached through get, locating
// every violation at the parent path extended by name. It uses no reflection.
func Field[T, F any](name string, get func(T) F, constraints ...Constraint[F]) (constraint Constraint[T], err error) {
	//: delegate to the service combinator.
	return svcvalidation.Field(name, get, constraints...)
}

// Each applies constraints to every element of a slice member of T reached
// through get, locating each element's violations at "name[i]".
func Each[T, E any](name string, get func(T) []E, constraints ...Constraint[E]) (constraint Constraint[T], err error) {
	//: delegate to the service combinator.
	return svcvalidation.Each(name, get, constraints...)
}

// Required refuses the zero value of T. It cannot distinguish "not supplied"
// from "supplied as zero" — declare the field as a pointer when that matters.
func Required[T comparable]() Constraint[T] {
	//: delegate to the service constraint.
	return svcvalidation.Required[T]()
}

// AtLeast refuses a value below lo; the bound is inclusive. It is the
// programmatic spelling of the `min=` struct tag, and both report the rule
// "min".
func AtLeast[T cmp.Ordered](lo T) Constraint[T] {
	//: delegate to the service constraint.
	return svcvalidation.AtLeast(lo)
}

// AtMost refuses a value above hi; the bound is inclusive. It is the
// programmatic spelling of the `max=` struct tag.
func AtMost[T cmp.Ordered](hi T) Constraint[T] {
	//: delegate to the service constraint.
	return svcvalidation.AtMost(hi)
}

// Between refuses a value outside [lo, hi]. lo > hi is refused at
// construction: an inverted interval is satisfied by nothing.
func Between[T cmp.Ordered](lo, hi T) (constraint Constraint[T], err error) {
	//: delegate to the service constraint.
	return svcvalidation.Between(lo, hi)
}

// Length refuses a string whose RUNE count falls outside [min, max]. max may
// be [Unbounded]. It counts runes, not grapheme clusters.
func Length(minRunes, maxRunes int) (constraint Constraint[string], err error) {
	//: delegate to the service constraint.
	return svcvalidation.Length(minRunes, maxRunes)
}

// Count refuses a slice whose element count falls outside [min, max]. max may
// be [Unbounded]. Count(1, Unbounded) is how presence is expressed for a slice.
func Count[E any](minLen, maxLen int) (constraint Constraint[[]E], err error) {
	//: delegate to the service constraint.
	return svcvalidation.Count[E](minLen, maxLen)
}

// OneOf refuses a value absent from allowed. An empty allowed set is refused
// at construction: it is a forgotten argument, not a permissive rule.
func OneOf[T comparable](allowed ...T) (constraint Constraint[T], err error) {
	//: delegate to the service constraint.
	return svcvalidation.OneOf(allowed...)
}

// Matches refuses a string the pattern does not match. The pattern is compiled
// at construction, and Go's RE2 has no backtracking — a pattern cannot be
// turned into a denial of service by the value it is shown.
func Matches(pattern string) (constraint Constraint[string], err error) {
	//: delegate to the service constraint.
	return svcvalidation.Matches(pattern)
}

// Struct compiles the `validate` tags of T into a Constraint, caching the plan
// per (type, mode).
//
// Accepted: required, min, max, minlen, maxlen, mincount, maxcount,
// oneof=a|b|c, dive. Refused BY NAME, each saying what to do instead: pattern
// / regex (a regexp cannot live in a comma-separated tag), email, url, uuid,
// dive into a map, and any unknown rule.
func Struct[T any](cfg StructConfig) (constraint Constraint[T], err error) {
	//: delegate to the service compiler.
	return svcvalidation.Struct[T](cfg)
}
