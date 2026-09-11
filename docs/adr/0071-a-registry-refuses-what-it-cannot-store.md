# ADR 0071 — a registry refuses a plug-in it cannot store, at the call that publishes it

- **Status**: Accepted
- **Date**: 2026-09-11
- **Deciders**: SDK maintainers
- **Related**: [ADR 0011](0011-kernel-snapshot-primitive.md) (the copy-on-write container every one of these registries is built on), [ADR 0010](0010-kernel-recycler-primitive.md) (a primitive admitted to consolidate code that already existed several times), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (refuse where the SDK cannot serve)

## Context

The SDK has fifteen `Register*` functions across eight `internal/core` packages
(`codec`, `crypto` ×8, `id`, `metrics`, `trace`, `transform`, `view`,
`writer`). Every one of them opens the same way:

```go
if c == nil {
    panic(…)
}
```

That guard is wrong in two ways, both measured on the real registry before this
ADR:

- **A typed nil passes it.** `transform.Register((*nilable)(nil))` panicked
  with nothing, stored the value, and `transform.Lookup` returned
  `(*transform_test.nilable)(nil)` — because an interface holding a typed nil
  pointer is not `== nil`. The process comes up. The failure arrives at the
  first dispatch through that scheme, arbitrarily far from the import that
  caused it and with a stack that names neither.
- **A non-comparable plug-in reaches the duplicate check**, which compares
  entries to tell an idempotent re-registration from a conflict. Registering a
  second plug-in of a type holding a slice under a taken name panicked with
  `runtime error: comparing uncomparable type transform_test.uncomparable` —
  Go's comparison reporting, not the domain's `DUPLICATE_REGISTRATION`, and it
  names neither the registry nor the key that collided.

Both are programming errors at **import time**: a registered plug-in is a
package-level variable initialiser, so nothing can recover and nothing can
choose differently at runtime. That is exactly why these functions panic.

## Decision

The question moves into one kernel primitive, `internal/kernel/plugin`, and
every registrar asks it:

```go
func Unusable(v any) (why string)
```

It returns the empty string for a value a registry can store, and a reason
fragment otherwise — `"nil *gzip.compressor"`,
`"transform.weirdPlug is not comparable"`. The fifteen registrars keep their
own dotted-quad code and reason and put the fragment after the colon, so a
refusal still carries the bracket header rule 4's log parser matches.

- **It answers with a reason, not a bool and not an error.** A bool erases
  which of the two shapes was refused, and they are found by different searches
  and fixed in different places. An error invites a registrar to return it,
  and there is nothing a caller can do with an error during package variable
  initialisation.
- **It is a kernel primitive** because what it judges is a property of Go's
  interfaces, not of any domain: it names no port and learns nothing about
  codecs or compressors. It arrives with fifteen in-tree consumers, which is
  ADR 0010's consolidation argument rather than ADR 0025's zero-consumer one —
  the guard existed fifteen times and was wrong the same way fifteen times.
- **Nil is reported before comparability.** A nil func plug-in is both, and
  "nil" is the more useful of the two answers.
- **Each package keeps its own code.** Several use
  `DUPLICATE_REGISTRATION` for what is not a duplicate; that inaccuracy
  predates this change and is left alone rather than turned into eight new
  serials in eight ranges in a change about something else.

## Consequences

- A typed nil or a non-comparable plug-in now fails at the `Register` call, in
  the importing package's initialiser, naming the concrete type. That is the
  earliest point at which the offender is still visible.
- Nothing on any hot path changed: `Unusable` runs once per registration.
- `reflect` enters the kernel for the first time in this package, at import
  time only.

## Breaking changes

Behavioural, for code that was already broken: a plug-in that used to be stored
and then fail at dispatch now fails at import. No API changes.

## Why not

- **Make the ports comparable by contract.** Go cannot state that in an
  interface. `comparable` constrains type parameters, and these registries hold
  interface values, whose comparability is a property of the dynamic type.
- **Drop the duplicate check so comparability stops mattering.** The check is
  what makes re-registering the same scheme idempotent — the shape every
  service package relies on for `var X = Register(x)` under multiple imports.
- **Recover around the comparison.** It converts a runtime panic into a caught
  runtime panic; the plug-in is still one nothing can compare, and the next
  registry operation finds it again.
- **A generic registry primitive in the kernel.** The registries differ in key
  type, in their error codes, and in extra indexes (codec's MIME and extension
  tables). What they share is this one question, and that is what moved.

## Deferred

- **An accurate code for the refusal**, per package — `DUPLICATE_REGISTRATION`
  is not what a typed nil is. It needs a serial in each of eight owned ranges
  plus the ADR 0005 registry table, and belongs in a change about error codes.

## References

- `internal/kernel/plugin/plugin.go`, and the fifteen registrars in
  `internal/core/{codec,crypto,id,metrics,trace,transform,view,writer}`.
- `TestUnusableNamesTheTwoShapesARegistryCannotStore` (kernel),
  `TestRegisterRefusesAPlugInTheRegistryCannotStore` (seven packages),
  `TestEveryRegistrarRefusesAPlugInTheRegistryCannotStore` (crypto's eight).
