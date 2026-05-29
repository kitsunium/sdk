# internal/service/writer/levelgate/

## Purpose

A `core/logger.Sink` decorator that enforces a per-writer severity floor:
records with `Level >= min` reach the wrapped sink; the rest are **silently
dropped** as a successful `(len(p), nil)` no-op. Used by every writer factory
(console, file, s3, cloudwatch) to realise the optional `MinLevel` config field
(ADR 0012) without the `route` middleware's `NoMatch` error — a dropped record
must never surface as a fan-out failure.

## Contents

| File | Role |
|---|---|
| `gate.go` | `gateSink` + `New(inner, min)` + `Write` / `Flush` / `Close` |

No `codes.go` / `errors.go` — the gate never originates an error.

## Behaviour

- `New(inner, min)` returns `inner` **unwrapped** when `min == level.Info` (the
  zero value of a config's `MinLevel`, i.e. "inherit the handler-global level")
  — no wrapper, no per-record overhead.
- For any other `min`, `Write` forwards records at/above the floor and drops the
  rest, reporting `(len(p), nil)` so `middleware/multi` never aggregates a
  spurious failure for a deliberately-dropped record.
- `Flush` / `Close` delegate verbatim — the gate buffers nothing and owns no
  resources.

## Do NOT

- Use this to express a per-writer floor of exactly `Info` while the handler
  global is lower — `min == Info` is the "inherit" sentinel and is unwrapped.
  Restrict via a higher level (`Warn` / `Error`) instead.
- Add buffering or I/O here — the gate is a pure pass-through filter.

## Verification

```
bazel test --config=race //internal/service/writer/levelgate:levelgate_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/levelgate/...
```
