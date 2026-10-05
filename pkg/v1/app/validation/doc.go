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
