# internal/core/statemachine/

## Purpose

The state-machine **domain** contract (**ADR 0120**): the `Store[E]` port a
machine reads and writes the caller's entities through, the `Journal[S]` port it
keeps its per-entity records in, and the values those ports speak —
`RecordValue`, `StepValue`, `Trigger`, `CreateEvent`. The engine lives in
`internal/service/statemachine`; the facade is `pkg/v1/statemachine`.

Code range: `0.2.56.*` (ADR 0120) — one code, `TriggerUnknown`.

## Contents

| File | Holds |
|---|---|
| `statemachine.go` | package doc; `Store[E]` (frozen at five), `Journal[S]` (frozen at three) |
| `record.go` | `CreateEvent`, `RecordValue[S]`, `StepValue[S]` (JSON-tagged, so a file journal can store them as they are) |
| `trigger.go` | `Trigger` + its five values, `String`, `ParseTrigger`; `CodeTriggerUnknown` / `TriggerUnknown` |

## Why this shape

**Absence is an answer, not an error.** `Get` reports `found`, `Insert`
`inserted`, `Replace` `replaced`. A store never has to produce a code of this
domain to say something ordinary — the caller's store keeps its own error
vocabulary — and an error from a port method always means the store FAILED.
`Replace` reporting `false` is the whole of "a transition never resurrects an
entity deleted while it ran".

**The ports are the caller's to implement.** A framework plugs its document
store in; a program without one writes five methods over a map or a table. So
both ports are frozen (ADR 0039): a new capability — a versioned replace, a
watch — arrives as a sibling interface reached by type assertion.

**`Journal.Save` and `Delete` are variadic.** A machine that opens reconciles
every entity at once; a journal that rewrites a whole file does it once per
call, not once per entity.

**The journal's order is per key.** The engine calls it one call at a time per
key and never under its bookkeeping mutex, so writes of different keys may
arrive at once: the port says so, and says a journal may read the machine but
never tell it anything.

**`RecordValue.History` is a slice, as the core values' collections are**
(`logger.RecordValue.Attrs`, `mail.MessageValue.To`). The engine hands every
caller — the journal, `Record`, `Records` — a copy it owns.

**`Trigger` has no text marshalling.** A value-receiver `String` beside a
pointer-receiver `UnmarshalText` is a receiver mix the linter refuses on a
non-struct type, so a JSON journal stores the stable NUMBER and `ParseTrigger`
reads the name back. The values are never renumbered; the zero value names no
trigger.

**No registry.** A store is a caller's collection; there is no name the SDK
could resolve (as for `lock`, `secret`).

## Do NOT

- Add a method to `Store` or `Journal`. Add a sibling interface.
- Renumber a `Trigger`: journals store the numbers.
- Put engine behaviour here — timers, locks, hooks are the service's.

## Verification

```sh
bazel test --config=race //internal/core/statemachine:statemachine_test
cd internal/core && GOWORK=off go test -race ./statemachine/
```
