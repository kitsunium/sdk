# pkg/v1/logger/slogbridge/

## Purpose

Adapts an SDK `logger.Logger` to `log/slog`, so a foreign API whose logging
knob is typed as the **concrete** `*slog.Logger` emits through the SDK
pipeline instead of a second one. `NewHandler` returns the `slog.Handler`;
`New` returns the `*slog.Logger` a caller hands to that API.

This is the ONE package in the SDK allowed to import `log/slog` (ADR 0032).
Kernel, core and service stay free of it — `internal/core/logger/level`
still says "specifically never log/slog", and that remains true.

## Why it exists

`mcp.ServerOptions.Logger` is a `*slog.Logger`, not an interface. Before this
package the only way to satisfy such an API was a SECOND logger writing to the
same stream, and the second pipeline is where the damage lives:

| Symptom | Cause |
|---|---|
| Records silently dropped | two thresholds parsed by two different rules — `ParseLevel` trims and lowercases, a hand-rolled `switch` usually does not, so `LOG_LEVEL=DEBUG` filters at Debug on one side and Info on the other |
| Unparseable log file | two line formats on one stream; no parser reads both |
| `framework_version` on half the records | only the SDK path stamps it |

One bridge removes all three at once, because there is only one pipeline left.

## Enforcement

The rule this package exists to serve is checkable in a consumer's repo:

```bash
go run github.com/kitsunium/sdk/tools/sdkguard@latest -level=invariant ./...
```

`SDK001` flags `slog.New` / `NewTextHandler` / `NewJSONHandler` / `SetDefault` /
`Default` — the constructs that build a second destination — while leaving the
`*slog.Logger` type and the attr constructors alone, since a consumer of this
package needs them (ADR 0033). `slogbridge.New` itself carries a documented
`//sdkguard:allow SDK001` directive: this is the one sanctioned bridge.

## Contents

| File | Role |
|---|---|
| `slogbridge.go` | package doc + `NewHandler` / `New` |
| `handler.go` | the `slog.Handler` implementation (`Enabled` / `Handle` / `WithAttrs` / `WithGroup`) |
| `convert.go` | `toLevel`, `qualify`, `appendAttr` / `appendGroup`, `convert` |
| `codes.go` | `CodeLoggerRequired` (range `1.1.1.*`) |
| `errors.go` | `LoggerRequired` sentinel |

`README.md` is generated from the package doc comment via `make docs-readme`
(ADR 0008).

## Conventions

- **The SDK Logger owns the threshold.** The bridge holds no level of its own,
  so `Enabled` delegates and a `LevelVar` retuned at runtime takes effect on the
  slog side too. A caller who used to set `slog.HandlerOptions.Level` moves that
  decision to the SDK Logger's `MinLevel`; there is deliberately no second knob
  to disagree with the first.
- **Levels convert exactly.** Both scales use `-4 / 0 / 4 / 8` for
  Debug/Info/Warn/Error, so the cast is value-preserving and a custom
  `slog.Level(2)` keeps its position. Only the width differs — the SDK `Level`
  is an `int8` — so out-of-range levels **saturate** rather than wrap. Wrapping
  is the dangerous direction: it would turn an absurdly severe level into a
  debug one.
- **Groups are flattened here, not delegated.** `WithGroup` tracks a prefix in
  the handler and qualifies keys itself; it does NOT call
  `Logger.WithGroup`. The two group models disagree: the SDK text handler
  applies its *final* group stack to every bound attr, so an attr bound BEFORE
  a group would be moved under it retroactively. slog's model is positional —
  a group governs only what is bound after it. Qualifying in the bridge
  reproduces slog's semantics exactly AND stays correct whichever SDK handler
  or encoder sits underneath. Pinned by
  `TestGroupsAreEndPositionalNotRetroactive`.
- **slog's elision rules live here.** The SDK handler has no notion of them, so
  the bridge drops a zero `Attr`, inlines an empty group name, and drops an
  empty group. `LogValuer` payloads are resolved before conversion — an
  unresolved one would land in `Any` and print as an opaque payload.
- **Kinds are preserved, not flattened to `Any`.** Routing everything through
  `Any` would compile and log, but would cost the encoders their type-aware
  rendering.

## Known limit

`slog.Record.Time` does NOT cross the bridge. `Logger.Log` takes no timestamp,
so the SDK handler stamps the record from its own clock at emit time. For live
logging the delta is sub-microsecond; for a **replayed** record (built now,
handled later) the original instant is lost. Callers replaying records should
carry the original time as an explicit attribute.

## Do NOT

- Give the bridge its own level knob. Two thresholds that can disagree is the
  exact defect this package was built to delete.
- Call `Logger.WithGroup` from `WithGroup` — see the group-model note above.
- Import `log/slog` anywhere else in the SDK. The confinement to this package
  is the whole point of ADR 0032; a second import site re-opens the question
  core settled.
- Return a working handler for a nil Logger. `LoggerRequired` (`1.1.1.1`) is
  deliberate: a bridge to nowhere would break the single-pipeline guarantee at
  the exact moment a caller trusted it.

## Verification

```bash
bazel test --config=race //pkg/v1/logger/slogbridge:slogbridge_test
# Fallback
cd pkg && GOWORK=off go test -race -cover ./v1/logger/slogbridge/...
# expected coverage: >= 90%
```
