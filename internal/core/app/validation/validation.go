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
