<!-- updated: 2026-05-18T14:30:00Z -->
# internal/core/logger/level/

## Purpose

Severity levels for log records. Single type (`Level int8`), four constants (`Debug=-4`, `Info=0`, `Warn=4`, `Error=8`), one method (`String() string`). Signatures mirror `log/slog` for ergonomic familiarity. Code range `0.2.17.*` reserved (ADR 0006); no codes emitted today.

## History — why it lives here, not in `kernel/`

Moved out of `internal/kernel/` on 2026-04-19. The kernel rule is two-pronged: **stdlib-only AND generic**. `level` satisfies the first half (no external imports) but **fails the second half** — `Debug` / `Info` / `Warn` / `Error` are logger-domain vocabulary. The audit `.claude/contexts/sdk-layer-placement-audit.md` documents the move; the rule `kernel = stdlib-only AND generic` is enforced by review (not a linter today).

Inlining `Level` into `internal/core/logger` was rejected because:
- it would force a namespace collision (`logger.Level` next to `logger.Logger`),
- it would clutter the interface package with a Stringer + four constants,
- the short call-site alias `level.Debug` is the more idiomatic ergonomic.

## Contents

`level.go` — `Level int8`, the four constants, `String()`. `level_external_test.go` — every `String()` window plus the `Debug < Info < Warn < Error` ordering invariant.

## Conventions

- **`Level` is `int8`** — arbitrary values are legal. Threshold windows: `[..,Info) → "DEBUG"`, `[Info, Warn) → "INFO"`, `[Warn, Error) → "WARN"`, `[Error, ..] → "ERROR"`. A value of `3` maps to `"INFO"`; `16` maps to `"ERROR"`.
- **Constants are spaced `-4 / 0 / 4 / 8`** so downstream levels (`Trace = Debug - 4`, `Critical = Error + 4`) can interpolate without renumbering — same scheme as `log/slog`.
- **Stdlib-only**; specifically, no `log/slog` import. `level` is the one package this SDK refuses to couple to slog's API.
- **`String()` is the only method.** Parsing belongs above this layer (service or `pkg/v1/logger`).

## Do NOT

- Add `Parse(string) (Level, error)` or `MarshalJSON` here — keep parsing/serialisation in the calling layer.
- Import `log/slog`.
- Introduce a second domain's severity here. If a future domain needs severity-like values, it declares its own type in its own core package.
- Renumber the existing constants — downstream code (and `slog` interop in `pkg/v1/logger`) relies on the exact `-4 / 0 / 4 / 8` spacing.

## Verification

```
# Primary (Bazel)
bazel test --config=race //internal/core/logger/level:level_test

# Fallback
cd internal/core && GOWORK=off go test -race -cover ./logger/level/...
# expected: 100% line coverage.
```
