// Package config — the schema declaration: what an author writes.
package config

import (
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// SchemaSpec declares a configuration's SHAPE: which keys the application
// reads, which of them it cannot start without, what each holds when nobody
// supplies it, and what the decoded result must satisfy.
//
// The zero value is a legitimate declaration. It requires nothing, defaults
// nothing, and adds no rule of its own — but the `validate` struct tags of T
// still apply, because a schema that ignored the tags on the very type it
// describes would be a second place to look for the same answer, and it still
// refuses an unknown key, because that is the check the zero value must not
// silently disarm. A configuration with nothing declared passes, which is
// ADR 0031's accepting half: "nothing is declared" and "something is declared
// wrongly" are opposite states and only the second is a defect.
type SchemaSpec[T any] struct {
	// Required names the keys a load must find in some source. A key listed
	// here and absent from every layer fails the LOAD — at start-up, before
	// anything reads the value — and every missing key is named in one error
	// rather than the first one found.
	//
	// It is a KEY-level statement, decided on the merged map while the key is
	// still a key. That is what distinguishes it from the validation domain's
	// `required` tag, which is a VALUE-level statement decided after the
	// decode, where an absent key and an explicit zero are the same bytes.
	// `port = 0` written in a file satisfies this and fails `validate:"required"`;
	// declare both when both are meant.
	//
	// A required key may not also carry a default: see Defaults.
	Required []string
	// Defaults are the typed values keys take when NO Source supplied them.
	// Each is checked at construction against the type, against the grammar,
	// and against the constraints the schema itself declares for that key.
	//
	// A key here may not also appear in Required — nor may either be nested
	// under the other. A required key the schema itself fills can never be
	// missing, so the requirement would be a clause that cannot fire, and a
	// clause that cannot fire is worse than no clause: it is read as
	// protection. The contradiction is refused at construction (ADR 0031).
	Defaults []coreconfig.DeclaredValue
	// AllowUnknownKeys stops the load refusing a key no field of T addresses.
	//
	// The zero value REFUSES, and that is the whole point: a schema is the
	// statement "these are the keys I read", so a key outside it is either a
	// typo or a deliberate extra. Ignoring it silently is the classic
	// production incident — `APP_PORTT=9090` decodes into nothing, the process
	// starts on the old port, and the only evidence is the absence of an
	// effect.
	//
	// Set it when the source genuinely carries more than this type reads: a
	// configuration file shared by two services, or an unprefixed EnvSource,
	// which hands over every variable in the process environment. Those are
	// facts about the SOURCE, and the opt-out is where the author says so.
	AllowUnknownKeys bool
	// Rule is one extra constraint, composed with the `validate` tags of T
	// into a single report. It is where a CROSS-FIELD rule lives — "a TLS
	// certificate without its key", "a replica count above the node count" —
	// because a per-field tag has no vocabulary for a second field and
	// inventing one is how a tag dialect turns into a language (ADR 0046).
	//
	// It is deliberately singular. The validation domain already ships the
	// combinator (validation.All), so a slice here would be a second spelling
	// of composition that the reader has to learn — and the one that cannot
	// express "all of these, but stop at the first" the way the real
	// combinators can. Pass validation.All(a, b) for several.
	//
	// A rule reports at core/validation.RootPath when no single key is at
	// fault, which is exactly what the grammar reserves the root for.
	Rule corevalidation.Constraint[T]
}
