# ADR 0032 — slog is an adapter at the public edge, never a dependency of the domain

- **Status**: Accepted
- **Date**: 2026-09-04
- **Deciders**: SDK maintainers
- **Related**: [ADR 0001](../adr/0001-sdk-go-multimodule-layout.md) (layer shape), [ADR 0002](../adr/0002-sdk-errors-package.md) (explicit construction over silent defaults), [ADR 0005](../adr/0005-sdk-error-codes-dotted-quad.md) (error codes — the `1.1.1.*` block is allocated in Decision 5 below, not by editing that ADR), [ADR 0008](../adr/0008-readme-from-code-generation.md) (README generation), [ADR 0030](../adr/0030-stdout-is-a-protocol-channel.md) (stdout is a protocol channel — the transport this defect is worst on)
- **Amends**: the "never log/slog" rule stated in `internal/core/logger/level/level.go` — it now binds the domain (kernel/core/service), not the public edge

## Context

`internal/core/logger/level` says it outright: stdlib-only, "and specifically
never log/slog". The rule is right and it stays. It keeps a foreign vocabulary
out of the domain, and it is why the SDK's `Logger` is our contract rather than
a thin coat of paint over someone else's.

But the rule was written as if the SDK only ever produced logs. It also has to
**hand a logger to other people's code**, and a large part of the Go ecosystem
types that hand-off as the concrete `*slog.Logger`, not as an interface:

```go
// github.com/modelcontextprotocol/go-sdk@v1.7.0/mcp/server.go:73
Logger *slog.Logger
```

No SDK `Logger` can be passed there. Before this ADR the only way out was a
second logger, built beside the first and pointed at the same stream:

```go
lg, _ := logger.NewText(logger.Config{Writer: os.Stderr, MinLevel: parseLevel(cfg.LogLevel)})
slg := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel(cfg.LogLevel)}))
```

That shape was observed in a downstream service and audited. It produces three
defects, and none of them announces itself:

| Symptom | Cause |
|---|---|
| Records silently dropped | two thresholds derived by two different rules. `ParseLevel` trims and lowercases; the hand-rolled `switch` beside it matched lowercase exactly. `LOG_LEVEL=DEBUG` therefore filtered at **Debug** on one side and **Info** on the other. |
| Log file no parser can read | two line formats interleaved on one stream: `TIME LEVEL msg k="v"` from the SDK, `time=… level=… msg=… k=v` from slog. |
| `framework_version` on half the records | only the SDK path stamps it. |

The severity is highest exactly where this hand-off is most common. Under a
stdio transport (ADR 0030) stderr is the *only* observability channel, so a
split stream is not an inconvenience — it is the whole signal, halved.

The deeper point: the second pipeline is not a mistake a careful author avoids.
It is what the layering *forces* on any consumer who meets a `*slog.Logger`
parameter. The rule was complete for the domain and incomplete for the edge.

## Decision

**1. `log/slog` is permitted in exactly one package: `pkg/v1/logger/slogbridge`.**

slog is treated as what it is — a foreign ecosystem the SDK adapts to, in the
same category as the AWS writers (ADR 0012) or the vendored codecs (ADR 0022).
Adapters to foreign ecosystems live at the public edge, opt-in by import, and
never leak inward. Kernel, core and service keep the "never log/slog" rule
verbatim; it now means what it always should have meant — *the domain* never
learns the word.

**2. The bridge adapts; it never adds policy.**

`NewHandler(lg)` returns an `slog.Handler` forwarding to `lg`; `New(lg)` returns
the `*slog.Logger` built on it. The bridge holds no level, no encoder, no sink,
no clock. Everything that could be configured twice is configured once, on the
SDK Logger. This is what makes the guarantee checkable rather than aspirational:
there is no second knob to drift.

**3. A nil Logger is refused, not absorbed.**

`LoggerRequired` (`1.1.1.1` / `LOGGER_REQUIRED`) rather than a silently
discarding handler — the ADR 0002 reasoning applied one layer out. A bridge to
nowhere would break the single-pipeline guarantee precisely where the caller
believed it held.

**4. Group semantics are reproduced in the bridge, not delegated.**

slog groups are *positional*: a group governs what is bound after it. The SDK
text handler applies its final group stack to every bound attr, so delegating
`WithGroup` would retroactively move an earlier attr under a later group.
The bridge therefore tracks the prefix itself and qualifies keys into the dotted
form both SDK encoders already emit. This also makes the bridge independent of
which handler or encoder sits underneath.

**5. The error block is allocated here, not by editing ADR 0005.**

`pkg/v1/logger/slogbridge` owns `1.1.1.*`, following the sub-package pattern
ADR 0005 §Registry already established (`pkg/v1/codec/baseenc` = `1.2.1.*`).
`CodeLoggerRequired` = `1.1.1.1`.

The allocation is recorded here because ADR 0005 is Accepted and
`docs/adr/CLAUDE.md` forbids editing a merged ADR's Decision — the rule ADR
0006 already followed when it extended the same registry. Nothing is lost by
keeping that table as it was: the executable source of truth is the AST audit
in `internal/kernel/errs` plus the generated `docs/error-codes.yaml`, which
enforce uniqueness mechanically and already carry this block. A row in a
markdown table never was what prevented a collision.

## Consequences / Semantics

- A consumer facing a `*slog.Logger` parameter now has a one-line answer, and
  the three defects above become unreachable rather than merely discouraged.
- `log/slog` enters the dependency graph only for consumers who import the
  subpackage. It is stdlib, so this costs nothing but the principle is kept:
  `pkg/v1/logger` itself is untouched.
- The SDK Logger's threshold becomes the only threshold. A caller who set
  `slog.HandlerOptions.Level` must move that decision to `MinLevel`. This is a
  deliberate removal of a knob, and the migration is mechanical.
- `slog.Record.Time` does not cross: `Logger.Log` takes no timestamp, so the SDK
  handler stamps at emit time. Sub-microsecond for live logging; lossy for
  replayed records. Documented in the package doc and its `CLAUDE.md` rather
  than papered over.
- Two pre-existing gaps had to be closed for the bridge to be faithful, and both
  were defects in their own right:
  - `internal/service/logger.TextHandler` rendered only 4 of the 8 `Kind`s,
    degrading Duration, Time and Uint64 to `?` — while `encoder/text.go` rendered
    all 7 non-group Kinds. The same file carried the table **twice**, and the two
    copies had already drifted from the encoder's. `appendAttr` now delegates to
    `appendValueOnly`, and the single remaining table matches the encoder.
    Without this, bridging slog would have printed `?` for durations, the
    attribute type slog callers use most.
  - `pkg/v1/logger` exposed `MemorySink` "so tests can assert on a record's
    Level, Message, and Attrs" but never re-exported `Kind`, so a consumer could
    read `Value.Kind()` and had no way to name the result. `kind.go` adds the
    alias and the nine constants the core already documents as "stable across
    the public API".

## Breaking changes

- **`TextHandler` output changes for three `Kind`s.** Attributes of kind
  Duration, Time and Uint64 used to render as `?` through `logger.NewText`; they
  now render their value, matching `encoder/text.go`. Any consumer parsing that
  output for a literal `?` is affected. The old behaviour was a defect — the
  file carried the value table twice and the copies had drifted — so this is a
  fix, but it is observable and belongs here rather than in a footnote.
- **No API is removed or changed.** `slogbridge` is a new opt-in subpackage;
  `kind.go` only adds aliases the core already documented as stable. Existing
  call sites compile unchanged.

## Alternatives considered

- **Put the bridge in `pkg/v1/logger` directly.** Rejected: every consumer of
  the façade would inherit the import, and the opt-in boundary is what keeps
  "the domain never learns slog" true as a statement about the import graph
  rather than about intent.
- **Make the SDK `Logger` an `slog.Handler`.** Rejected: it inverts the
  dependency, dragging slog's `Record`, `Value` and `Attr` into core — precisely
  what the level package's rule exists to prevent.
- **Do nothing; document the two-logger pattern.** Rejected: the audit that
  prompted this ADR found a service where the documented-by-omission pattern had
  produced all three defects simultaneously, in production, unnoticed. A pattern
  that fails silently every time it is used is not a pattern.
- **Delegate `WithGroup` to `Logger.WithGroup`.** Rejected: it is the shorter
  code and the wrong semantics. See Decision 4.

## Deferred

- **Carrying `slog.Record.PC` across the bridge.** The bridge calls the ordinary
  `Log`, which captures a program counter at the bridge itself, so a destination
  built with `logger.WithCaller` reports `slogbridge/handler.go` rather than the
  foreign library's logging call. Closing it needs an emission path that ACCEPTS
  a program counter; `Logger.Log` captures its own, and giving it one touches the
  contract every handler implements. Documented under §Known limits in the
  package, with the workaround — do not enable `WithCaller` on a destination that
  receives bridged records, because a wrong source is worse than none.
- **Carrying `slog.Record.Time` across the bridge.** It needs an emission path
  that accepts a timestamp — `Logger.Log` has none, and adding one touches the
  core contract every handler implements. Out of scope for an adapter; revisit
  if a replay or ingestion use case makes the lost instant matter.
- **A `KindGroup`-aware encoder.** Groups are flattened to dotted keys today
  because neither bundled encoder renders a group payload. A JSON encoder that
  nests them would be closer to slog's own JSON output, and would make the
  flattening a choice rather than a constraint.
- **Reconsidering the "never log/slog" rule for `core`.** This ADR moves the
  boundary rather than the rule. If a second foreign ecosystem ever needs the
  same treatment, the question of whether core should expose a neutral
  handler-shaped port is worth reopening.

## References

- [ADR 0002](../adr/0002-sdk-errors-package.md) — explicit construction over silent defaults, the reasoning behind `LoggerRequired`
- [ADR 0007](../adr/0007-sdk-release-and-versioning.md) — patch releases carry `internal/*` fixes
- [ADR 0017](../adr/0017-pkg-bare-module-path.md) — why the public module is the bare `…/pkg`
- [ADR 0030](../adr/0030-stdout-is-a-protocol-channel.md) — the transport where a split log stream hurts most
- [ADR 0033](../adr/0033-consumer-rule-enforcement.md) — how this rule is enforced on consumers
- `internal/core/logger/logger.go` — the `Logger` port the bridge forwards to
- `pkg/v1/logger/slogbridge/CLAUDE.md` — the package's own conventions and limits
- Go stdlib, [`log/slog.Handler`](https://pkg.go.dev/log/slog#Handler) — the contract implemented, including the attribute elision rules the bridge reproduces
