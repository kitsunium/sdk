// Package validation — the ordered-value bounds constraints.
//
// Package validation — the struct-tag compiler. Everything here runs once per
// type, at construction; nothing here runs during a validation.
//
// Package validation — the descent combinators. These are what make a
// violation LOCATED on a nested structure, and they do it with an accessor
// function rather than reflection: the programmatic path never imports
// reflect, so a hot validator is a chain of direct field reads the compiler
// can inline.
//
// Package validation — compiling the `dive` descent.
//
// Descent is EXPLICIT. A nested struct is walked only when its field says
// dive, never automatically: automatic recursion would walk into time.Time,
// net.IP and every other struct that happens to be a field, and a rule engine
// that silently reaches places the author did not name is a rule engine whose
// report cannot be trusted.
//
// Package validation — hosts elementPlan, the rule set one element of a dived
// slice or array is checked against.
//
// Package validation — one embedded struct encoding/json descends into.
//
// Package validation — which field a JSON key actually decodes into.
//
// Package validation — the regular-expression constraint.
//
// Package validation — the compiled struct-tag plan and its per-type cache.
//
// A plan is compiled ONCE per (type, mode) and reused by every validation of
// that type. Parsing a struct tag on every request would put string splitting
// and reflect.StructField lookups on the hot path of every field of every
// request — which is exactly the shape of the "we added validation and the p99
// moved" post-mortem. See BENCH.md for what the cache is worth.
//
// Package validation — hosts planKey, the identity of a compiled plan.
//
// Package validation — the presence constraint.
//
// Package validation — a field the plan puts rules on, located for the reach
// check.
//
// Package validation — the refusal helpers. Every refusal names what was
// wrong in structured fields; the Public message stays a fixed literal.
//
// Package validation — the closed set of built-in rule names. A rule name is
// part of the published contract: it is what a ViolationValue.Rule carries,
// what an application keys its own translated messages on, and what a validate
// struct tag spells. Adding one is a decision the SDK maintains forever.
//
// Package validation — the tag dialect: what a rule item may say, and what it
// is refused for saying.
//
// Package validation — the kind-bound halves of the tag dialect. Each builder
// resolves the field's kind ONCE, at compile time, and returns a closure that
// reads the value through the one accessor that kind supports. A validation
// therefore performs no type dispatch at all.
//
// Package validation — the tag half of set membership.
//
// Package validation — the set-membership constraint.
//
// Package validation — the size constraints: how long a string is, and how
// many elements a slice holds. Two rules rather than one, because "at most 30
// characters" and "at most 30 entries" are different requirements with
// different fixes, and Go's type system cannot spell len over both anyway.
//
// Package validation — the struct-tag front end.
//
// Package validation implements the SDK's constraint engine over the
// internal/core/app/validation port: a small, closed set of built-in constraints,
// the combinators that compose them and descend into nested structures, and a
// struct-tag front end that compiles a cached plan per type (ADR 0046).
//
// Two entry shapes, one contract. The PROGRAMMATIC path is generic and uses no
// reflection at all — Field and Each take an accessor function, so every
// descent is a direct field read the compiler can inline. The TAG path trades
// that for ergonomics and is measured rather than assumed; see BENCH.md.
//
// Both produce a core/app/validation.Constraint, so they compose with each other:
// a struct-tag validator and a hand-written cross-field rule can be handed to
// All and appear in one report.
//
// Collecting EVERY violation is the default and First is the opt-in. A form
// that reports one error at a time makes the user submit it five times.
//
// Package validation — the single place a ViolationValue is minted, so every
// built-in constraint reports the same shape.
package validation
