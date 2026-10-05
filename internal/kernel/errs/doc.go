// Package errs — provides package-level introspection helpers
// that walk a cause chain and read fields from the deepest *Error
// encountered. Consumers of pkg/v1/errs use re-exports of these.
//
// Package errs — defines the dotted-quad Code type introduced by
// ADR 0005. Layout: MM.LL.PP.SS over uint32 — Major.Layer.Package.Serial.
// Codes are comparable, ordered, map-keyable, and const-expressible via
// hex literals (Pack is a runtime constructor only).
//
// Code is a packed uint32 (shifts/masks only), so it is fully portable across
// 32- and 64-bit GOARCH — there is no build constraint here. Validation bounds
// every Code to uint32 ≤ 0x7FFFFFFF (validate.go), so the rare int(code) cast
// round-trips losslessly even where int is 32-bit.
//
// Package errs — meta-codes (Layer=0 reserved for this file).
// Used in Define panic messages and in internal bootstrap errors
// (newValidationError). These codes are documentary: they are NOT
// returned from emitter APIs as sentinel *Error values.
//
// Layer=0 is ENFORCED reserved for this package by validateDefineArgs —
// any Define call with Layer==0 AND code not in this whitelist will panic.
//
// Constants are typed as Code so Define call sites accept them directly
// and downstream comparisons with Error.Code() are type-safe.
//
// Package errs — defines the SDK-wide typed Error plus the Define
// and Wrap constructors. Consumers never import this package directly —
// they introspect errors through github.com/kitsunium/sdk/pkg/v1/errs.
//
// Package errs — defines the closed FieldValue union used to
// attach structured metadata to an Error without opening an `any` back
// door. FieldValue is a transport + textual-restitution contract, NOT a
// vehicle for strongly-typed reconstruction on the consumer side —
// StringValue is the only value accessor exposed in this MR.
//
// Package errs — provides ParseCode, the strict canonical parser
// for dotted-quad Code strings produced by Code.String(). The parser is
// intentionally NOT compatible with the Padded() form — that would make
// two textual representations round-trip to the same Code, violating the
// single-canonical-form invariant declared in ADR 0005 §3.2.
//
// Package errs — hosts the PrefixMatcher target used by
// errors.Is for CIDR-style Code matching. PrefixMatcher deliberately does
// NOT implement the `error` interface so it cannot escape into error
// chains as a return value — callers pass it only to errors.Is.
//
// Package errs — placeholder companion to the SDK-wide AST audit
// (registry_external_test.go). There is no runtime registry today; the
// code allocation table lives in ADR 0002 and the audit test embeds it.
//
// Package errs — holds the wrap-trail mechanism introduced by
// ADR 0005. Trail carries the sites a *Error was wrapped through; origin
// wins on Code() per ADR 0002, but Trail() / HasCode() traverse the full
// list of wrap sites to help observability and matching.
//
// Package errs — TrailOf exposes the wrap-trail codes of the deepest *Error in
// a chain, the read-side companion to the Of-family accessors, enabling
// structured log decomposition without reparsing the rendered bracket header.
//
// Package errs — centralises the runtime structural checks
// applied by Define. Factoring the logic out of Define lets tests cover
// every rule as a plain function call without subprocess fixtures for
// package-init panics.
//
// Package errs — hosts WrapParams in its own file so error.go keeps a
// single exported struct (one-struct-per-file convention).
package errs
