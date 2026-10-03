<!-- updated: 2026-10-03T12:00:00Z -->
# internal/kernel/plugin/

## Purpose

One question, asked by every process-wide registry in the SDK before it
publishes anything: **is this value usable as a registry entry at all?**

`Unusable[T any](v T) string` returns the empty string when it is, and a
reason fragment when it is not. `T` is the registrar's port, inferred at the
call; what is judged is the dynamic value behind it, which `reflect` reads
through the boxing. ADR 0071.

And, once that question is answered, **where the entry goes**: `Registry[K, V]`
is the read-mostly, name-keyed, copy-on-write table a registrar publishes into
— `Publish` / `Claim` / `Lookup` / `Names`. It REPORTS a conflict and never
builds an error, for the reason `Unusable` returns a string: the registrar
refuses with its own code. Every process-wide registry in the core is an
instance of it (ADR 0159): the metrics and trace exporter registries first,
then the six that each carried a copy of the mechanism — `core/data/codec`
(its `Format` table and, beside it, its MIME and extension indexes),
`core/observe/logger/writer`, `core/crypto` (through its one registrar,
`schemeRegistry`, and the AEAD wire-id index), `core/data/transform`,
`core/app/id` and `core/app/view`. What each kept is its refusal: its code, its
reason, the fields that name the key.

Code range: none. The answer is a string, not an error — see below.

## Contents

| File | Surface |
|---|---|
| `plugin.go` | `Unusable` — the entry guard |
| `registry.go` | `Registry[K cmp.Ordered, V comparable]` — `Publish` (conflict reported, identical value idempotent, check-and-publish atomic), `Claim` (the holder of a taken name reported, the identical value included — for a registrar strict on every second registration, or naming the holder), `Lookup` (zero value AND false on a miss), `Names` (ascending, the caller's own slice, one allocation) — over `kernel/concur/snapshot` |
| `plugin_external_test.go` | both refusals, both acceptances, and the comparison a caller is about to make |
| `registry_external_test.go` | the three publish outcomes, the holder a claim reports, the two misses, the order of `Names`, racing writers losing nothing, and racing claims on one name having one winner |
| `registry_internal_test.go` | the copy a publish makes leaves the source a reader may be walking untouched |
| `registry_bench_test.go` | `Lookup` hit, miss and parallel, each beside the hand-written snapshot read the core registries carried; `Names` — see `BENCH.md` |

## The two shapes the compiler accepts and a registry cannot store

A registry stores values behind an interface and hands them back to every
caller for the life of the process. Two shapes satisfy such an interface at
compile time and cannot serve:

- **A typed nil.** `(*gzipCompressor)(nil)` is not `== nil`, so the guard every
  registrar wrote (`if c == nil`) let it through. Measured on the real
  registry before this package existed: `transform.Register` stored it and
  `Lookup` returned `(*transform_test.nilable)(nil)`. The failure then surfaces
  at the first dispatch, arbitrarily far from the import that caused it.
- **A value whose dynamic type is not comparable** — a struct holding a slice,
  a map or a function. Registries compare entries to tell an idempotent
  re-registration from a conflict, and `==` on such a value is a runtime panic:
  `runtime error: comparing uncomparable type transform_test.uncomparable`.
  That is Go's comparison reporting, not the domain's `DUPLICATE_REGISTRATION`,
  and it names neither the registry nor the duplicate key.

Both are programming errors at **import time**, which is the whole reason the
registrars answer them with a panic rather than an error.

## Conventions

- **It returns a reason, not an error and not a bool.** The registrar panics
  with its OWN dotted-quad code and reason — `CODEC_NIL`, `WRITER_NIL`,
  `DUPLICATE_REGISTRATION` — and only borrows the sentence that says why. A
  `bool` would erase the distinction between the two shapes, which are found by
  different searches and fixed in different places; an `error` would invite a
  registrar to return it, and there is nothing a caller could do with it at
  package-variable initialisation time.
- **The reason names the concrete type**, because the registrar's own message
  names only the port: `"nil *gzip.compressor"` beside `codec.Register`.
- **Nil is checked before comparability.** A nil func plug-in is both, and
  `"nil plugin_test.funcPlug"` is the more useful of the two answers.
- **`reflect` is used and that is fine here.** It runs once per registration,
  at import, and nothing on any hot path calls it.

## Rules from ADR 0071

Superseded by ADR 0154 (the charter); ADR 0071 stays as the incident's record, and its rule lives here.

- **A registry asks `Unusable` before it stores, at the call that publishes** —
  the importing package's initialiser, the earliest point the offender is still
  visible — and panics with its OWN code and reason, borrowing only the sentence.
- **Two refusals, nil first**: a typed nil, and a value whose dynamic type is not
  comparable.
- *Lesson*: fifteen registrars guarded with `if x == nil` and were wrong the same
  way fifteen times — a typed nil was stored and failed at its first dispatch,
  and a non-comparable plug-in panicked inside the duplicate check with Go's
  message instead of the domain's.

## Do NOT

- **Add a "well-known port" check.** This package has no vocabulary: it never
  learns what a Codec or a Compressor is, which is why it can serve all of them.
- **Teach `Registry` a domain's refusal.** It reports a conflict (`Publish`)
  or the holder (`Claim`); the registrar panics or returns with ITS code, and
  asks `Unusable` before it publishes, because `==` on a non-comparable dynamic
  value panics inside the duplicate check with Go's message instead of the
  registrar's.
- **Read a holder back with a second `Lookup` to name it in a refusal.** `Claim`
  reports it from the step that refused; a `Lookup` after the fact reads
  another snapshot.
- **Grow `Registry` an index a single registry needs.** Registries differ in
  extra indexes (codec's MIME and extension tables) and in their error codes;
  what they share is the name-keyed copy-on-write table, and a registry with a
  second index keeps it beside this one rather than inside it.
- **Call `Unusable` on a hot path.** It is an import-time guard. (`Lookup` is
  the read every dispatch makes: one atomic load and a map read, no lock — at
  the cost of the hand-written read it replaced, `BENCH.md`.)

## Verification

```
bazel test --config=race //internal/kernel/plugin:plugin_test
# OR
cd internal/kernel && GOWORK=off go test -race ./plugin
# the numbers in BENCH.md
cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem -count=10 ./plugin
```
