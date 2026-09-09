// Package validation declares the SDK's value-checking port: the [Constraint]
// that inspects one value, the [ViolationValue] it reports when that value is
// not acceptable, and the [ReportValue] that collects them. A core sibling
// admitted by ADR 0046.
//
// The port is a FUNCTION type rather than an interface — the shape
// internal/core/CLAUDE.md already admits for resilience.Operation and
// scheduler.Job/Schedule. ADR 0039's rule is that a published port must not
// grow a method, because pkg/v1 aliases publish the shape and Go interfaces are
// structural; a func type satisfies that rule structurally, since it cannot
// grow a method at all.
//
// # What this package is not
//
// It is not internal/core/config.Validator. That port is a decoded config
// struct's SELF-check — `Validate() error`, one boolean answer, called once by
// config.Load. This package is the engine that answers it: a struct implements
// config.Validator by running its constraints and returning ReportValue.Err().
// The two compose; neither replaces the other.
//
// The concrete constraints (presence, bounds, length, set membership, pattern),
// the combinators that descend into fields and elements, and the struct-tag
// front end live in internal/service/validation. This package owns the
// contract, the two domain values, the path grammar, and the typed sentinels.
package validation

// Constraint reports what is wrong with value, at the path by which value was
// reached. A value it accepts yields a nil [ReportValue] — the acceptance path
// therefore allocates nothing, which matters because it is the path taken on
// every field of every valid request.
//
// path is the position of value inside the structure being validated, in the
// grammar [JoinField] / [JoinIndex] define; [RootPath] means "the value itself".
// A Constraint MUST propagate that path into every ViolationValue it reports,
// and MUST NOT invent a different grammar — the path is the whole reason a
// violation is actionable on a nested structure.
//
// Implementations MUST be pure and safe for concurrent use: the same value at
// the same path always yields the same report, and one compiled Constraint is
// shared by every goroutine validating that type.
//
// A Constraint MUST NOT copy the VALUE it rejected into the report. It names
// the rule and the bound; the value may be a password, a token or a national
// identifier, and a validation message is the one error message in a service
// that is designed to reach the end user. See [ViolationValue].
type Constraint[T any] func(path string, value T) ReportValue
