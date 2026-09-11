# internal/kernel/plugin/

## Purpose

One question, asked by every process-wide registry in the SDK before it
publishes anything: **is this value usable as a registry entry at all?**

`Unusable(v any) (why string)` returns the empty string when it is, and a
reason fragment when it is not. ADR 0071.

Code range: none. The answer is a string, not an error — see below.

## Contents

| File | Surface |
|---|---|
| `plugin.go` | `Unusable` — the whole package |
| `plugin_external_test.go` | both refusals, both acceptances, and the comparison a caller is about to make |

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

## Do NOT

- **Add a "well-known port" check.** This package has no vocabulary: it never
  learns what a Codec or a Compressor is, which is why it can serve all of them.
- **Widen it into a registry.** The registries differ in key type, in extra
  indexes (codec's MIME and extension tables) and in their error codes; what
  they share is this one question.
- **Call it on a hot path.** It is an import-time guard.

## Verification

```
bazel test --config=race //internal/kernel/plugin:plugin_test
# OR
cd internal/kernel && GOWORK=off go test -race ./plugin
```
