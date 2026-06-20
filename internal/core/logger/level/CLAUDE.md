<!-- updated: 2026-05-18T14:30:00Z -->
# internal/core/logger/level/

## Purpose

Severity levels for log records, plus the minimal vocabulary the logger and its
config layer need around them: the `Level int8` type with four constants
(`Debug=-4`, `Info=0`, `Warn=4`, `Error=8`) and `String()`, the `Leveler`
interface (`Level() Level`), the atomic `Var` holder (`NewVar`/`Set`/`Level`),
and the canonical string parser `ParseLevel`. Signatures mirror `log/slog` for
ergonomic familiarity. Code range `0.2.17.*` (ADR 0006); one sentinel emitted
today — `LevelUnknown` (`0.2.17.1`) from `ParseLevel`.

## History — why it lives here, not in `kernel/`

Moved out of `internal/kernel/` on 2026-04-19. The kernel rule is two-pronged: **stdlib-only AND generic**. `level` satisfies the first half (no external imports) but **fails the second half** — `Debug` / `Info` / `Warn` / `Error` are logger-domain vocabulary. The audit `.claude/contexts/sdk-layer-placement-audit.md` documents the move; the rule `kernel = stdlib-only AND generic` is enforced by review (not a linter today).

Inlining `Level` into `internal/core/logger` was rejected because:
- it would force a namespace collision (`logger.Level` next to `logger.Logger`),
- it would clutter the interface package with a Stringer + four constants,
- the short call-site alias `level.Debug` is the more idiomatic ergonomic.

## Contents

- `level.go` — `Level int8`, the four constants, `String()`.
- `leveler.go` — `Leveler` interface (`Level() Level`); lets a gate hold either a live `Var` or a constant `Level`.
- `var.go` — `Var`, an atomic `Level` holder (`NewVar`/`Set`/`Level`) for runtime-adjustable thresholds.
- `parse.go` — `ParseLevel(name) (Level, error)`, the canonical case-insensitive string→`Level` parser.
- `codes.go` / `errors.go` — `CodeLevelUnknown` (`0.2.17.1`) + the `LevelUnknown` sentinel `ParseLevel` returns on an unknown name.
- `level_compliance.go` — interface-assertion (`KTN-IFACE-ASSERT-PLACEMENT`).

Tests: `level_external_test.go` (`String()` windows + `Debug < Info < Warn < Error` ordering), `parse_external_test.go` (parse + round-trip), `var_*_test.go` (concurrent `Var`), `leveler_*_test.go`.

## Conventions

- **`Level` is `int8`** — arbitrary values are legal. Threshold windows: `[..,Info) → "DEBUG"`, `[Info, Warn) → "INFO"`, `[Warn, Error) → "WARN"`, `[Error, ..] → "ERROR"`. A value of `3` maps to `"INFO"`; `16` maps to `"ERROR"`.
- **Constants are spaced `-4 / 0 / 4 / 8`** so downstream levels (`Trace = Debug - 4`, `Critical = Error + 4`) can interpolate without renumbering — same scheme as `log/slog`.
- **Stdlib-only**; specifically, no `log/slog` import. `level` is the one package this SDK refuses to couple to slog's API.
- **`ParseLevel` is the one canonical parser.** It lives here (not in the calling layer) so every consumer — `logger.FromConfig`, `Var`, CLI flags — shares one string↔`Level` mapping and one `LevelUnknown` error. Serialisation (e.g. `MarshalJSON`) still stays out: that is a `pkg/v1/logger` concern.

## Do NOT

- Add `MarshalJSON` / `UnmarshalJSON` here — serialisation is a `pkg/v1/logger` concern; this package owns only the `string`↔`Level` parse (`ParseLevel`).
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

## Accepted audit findings

- Deferred/accepted low+info audit findings (V14) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
